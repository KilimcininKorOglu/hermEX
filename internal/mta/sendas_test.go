package mta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/smtp"
)

// fakeIdentifier is a directory.Accounts that also enumerates a fixed identity
// set, so send-as authorization can be exercised with aliases.
type fakeIdentifier struct {
	directory.StaticAccounts
	idents map[string][]string
}

func (f fakeIdentifier) Identities(user string) ([]string, error) {
	return f.idents[strings.ToLower(user)], nil
}

// resolveOnly is a directory.Accounts that does NOT enumerate identities, used
// to prove send-as fails closed to the authenticated user alone.
type resolveOnly map[string]string

func (r resolveOnly) Resolve(addr string) (string, bool) {
	p, ok := r[strings.ToLower(addr)]
	return p, ok
}

// TestSubmissionSendAsAuthorization proves an authenticated submission may only
// use an envelope sender it owns — its own login or an enumerated alias — so an
// authenticated account cannot spoof another sender. The match is
// case-insensitive and the null sender is refused.
func TestSubmissionSendAsAuthorization(t *testing.T) {
	accounts := fakeIdentifier{
		StaticAccounts: directory.StaticAccounts{"alice@test": {MailboxPath: "/x"}},
		idents:         map[string][]string{"alice@test": {"alice@test", "sales@test"}},
	}
	tests := []struct {
		name    string
		from    string
		wantErr bool
	}{
		{"own address", "alice@test", false},
		{"owned alias", "sales@test", false},
		{"case-insensitive", "Alice@Test", false},
		{"foreign sender", "bob@test", true},
		{"null sender", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &session{accounts: accounts, authUser: "alice@test"}
			err := s.Mail(tt.from, smtp.MailParams{})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Mail(%q) error = %v, wantErr %v", tt.from, err, tt.wantErr)
			}
			if !tt.wantErr && s.from != tt.from {
				t.Errorf("accepted From recorded as %q, want %q", s.from, tt.from)
			}
		})
	}
}

// TestUnauthenticatedMailUnrestricted proves inbound intake (no AUTH) accepts any
// envelope sender — send-as authorization applies only to authenticated
// submission, never to a relaying remote MTA.
func TestUnauthenticatedMailUnrestricted(t *testing.T) {
	s := &session{accounts: directory.StaticAccounts{}}
	if err := s.Mail("anybody@external", smtp.MailParams{}); err != nil {
		t.Fatalf("unauthenticated MAIL FROM refused: %v", err)
	}
}

// TestSendAsFailsClosed proves that when the directory cannot enumerate
// identities, an authenticated user may still send as themselves but as no one
// else.
func TestSendAsFailsClosed(t *testing.T) {
	accounts := resolveOnly{"alice@test": "/x"}
	if s := (&session{accounts: accounts, authUser: "alice@test"}); s.Mail("alice@test", smtp.MailParams{}) != nil {
		t.Error("send-as-self refused under fail-closed directory")
	}
	if s := (&session{accounts: accounts, authUser: "alice@test"}); s.Mail("sales@test", smtp.MailParams{}) == nil {
		t.Error("send-as-other allowed under fail-closed directory; must be refused")
	}
}

// TestSendAsGrantAuthorizes proves an authenticated user may put another mailbox in
// the envelope sender when that mailbox has granted them send-as, and may not
// otherwise. The grant lives on the target mailbox's store.
func TestSendAsGrantAuthorizes(t *testing.T) {
	pathA := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSendAs([]string{"bob@test"}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	accounts := fakeIdentifier{
		StaticAccounts: directory.StaticAccounts{
			"alice@test": {MailboxPath: pathA},
			"bob@test":   {MailboxPath: filepath.Join(t.TempDir(), "bob")},
			"carol@test": {MailboxPath: filepath.Join(t.TempDir(), "carol")},
		},
		idents: map[string][]string{
			"bob@test":   {"bob@test"},
			"carol@test": {"carol@test"},
		},
	}
	// bob, granted send-as on alice, may put alice in the From.
	if s := (&session{accounts: accounts, authUser: "bob@test"}); s.Mail("alice@test", smtp.MailParams{}) != nil {
		t.Error("granted send-as refused")
	}
	// carol, not granted, may not.
	if s := (&session{accounts: accounts, authUser: "carol@test"}); s.Mail("alice@test", smtp.MailParams{}) == nil {
		t.Error("ungranted user sent as alice; must be refused")
	}
}

// TestSendAsGrantMatchesGranteeAlias proves the grant is honored when it names any of
// the grantee's identities — a grant to an alias authorizes the owner of that alias.
func TestSendAsGrantMatchesGranteeAlias(t *testing.T) {
	pathA := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSendAs([]string{"robert@test"}); err != nil { // bob's alias
		t.Fatal(err)
	}
	st.Close()

	accounts := fakeIdentifier{
		StaticAccounts: directory.StaticAccounts{
			"alice@test": {MailboxPath: pathA},
			"bob@test":   {MailboxPath: filepath.Join(t.TempDir(), "bob")},
		},
		idents: map[string][]string{"bob@test": {"bob@test", "robert@test"}},
	}
	if s := (&session{accounts: accounts, authUser: "bob@test"}); s.Mail("alice@test", smtp.MailParams{}) != nil {
		t.Error("send-as grant to the grantee's alias was not honored")
	}
}

// TestSendAsFailsClosedOnUnopenableStore proves the grant denies when the target
// mailbox resolves but its store cannot be opened — the fail-closed security branch.
// A regular file where the mailbox directory should be makes Open's MkdirAll fail, so
// the test exercises the Open-fails path, not merely the empty-list path.
func TestSendAsFailsClosedOnUnopenableStore(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-store")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err := objectstore.Open(blocker); err == nil {
		st.Close()
		t.Fatal("precondition: Open succeeded on a file path; cannot exercise the broken-store branch")
	}
	accounts := resolveOnly{"victim@test": blocker}
	if s := (&session{accounts: accounts, authUser: "bob@test"}); s.Mail("victim@test", smtp.MailParams{}) == nil {
		t.Error("send-as allowed when the target store could not be opened; must fail closed")
	}
}

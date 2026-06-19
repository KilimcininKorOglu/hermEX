package mta

import (
	"strings"
	"testing"

	"hermex/internal/directory"
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
			err := s.Mail(tt.from)
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
	if err := s.Mail("anybody@external"); err != nil {
		t.Fatalf("unauthenticated MAIL FROM refused: %v", err)
	}
}

// TestSendAsFailsClosed proves that when the directory cannot enumerate
// identities, an authenticated user may still send as themselves but as no one
// else.
func TestSendAsFailsClosed(t *testing.T) {
	accounts := resolveOnly{"alice@test": "/x"}
	if s := (&session{accounts: accounts, authUser: "alice@test"}); s.Mail("alice@test") != nil {
		t.Error("send-as-self refused under fail-closed directory")
	}
	if s := (&session{accounts: accounts, authUser: "alice@test"}); s.Mail("sales@test") == nil {
		t.Error("send-as-other allowed under fail-closed directory; must be refused")
	}
}

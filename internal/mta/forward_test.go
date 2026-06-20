package mta

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
)

// forwardingAccounts is a StaticAccounts that also answers Forwarder, so a delivery
// test can attach mail-forward directives to specific local users without a database.
type forwardingAccounts struct {
	directory.StaticAccounts
	forwards map[string]directory.ForwardInfo
}

func (f forwardingAccounts) GetForward(address string) (directory.ForwardInfo, bool, error) {
	fi, ok := f.forwards[strings.ToLower(address)]
	return fi, ok, nil
}

// inboxCount opens a mailbox and returns its inbox message count (0 for a never-used
// mailbox, whose store is created empty on open).
func inboxCount(t *testing.T, mbox string) int {
	t.Helper()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

var fwdRaw = []byte("Subject: hi\r\n\r\nhello\r\n")

// TestForwardCCKeepsLocalAndCopies: a CC forward delivers to the recipient AND a copy
// to the destination.
func TestForwardCCKeepsLocalAndCopies(t *testing.T) {
	alice := filepath.Join(t.TempDir(), "alice")
	carol := filepath.Join(t.TempDir(), "carol")
	accounts := forwardingAccounts{
		StaticAccounts: directory.StaticAccounts{
			"alice@local": {MailboxPath: alice},
			"carol@local": {MailboxPath: carol},
		},
		forwards: map[string]directory.ForwardInfo{
			"alice@local": {Type: directory.ForwardCC, Destination: "carol@local"},
		},
	}
	unresolved, err := DeliverAndRelay(accounts, nil, "sender@remote", []string{"alice@local"}, fwdRaw, time.Now())
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("CC deliver = unresolved %v, err %v; want none", unresolved, err)
	}
	if n := inboxCount(t, alice); n != 1 {
		t.Errorf("alice inbox = %d, want 1 (CC keeps the local copy)", n)
	}
	if n := inboxCount(t, carol); n != 1 {
		t.Errorf("carol inbox = %d, want 1 (CC forwards a copy)", n)
	}
}

// TestForwardRedirectDropsLocal: a Redirect delivers only to the destination, not the
// recipient's own mailbox.
func TestForwardRedirectDropsLocal(t *testing.T) {
	alice := filepath.Join(t.TempDir(), "alice")
	carol := filepath.Join(t.TempDir(), "carol")
	accounts := forwardingAccounts{
		StaticAccounts: directory.StaticAccounts{
			"alice@local": {MailboxPath: alice},
			"carol@local": {MailboxPath: carol},
		},
		forwards: map[string]directory.ForwardInfo{
			"alice@local": {Type: directory.ForwardRedirect, Destination: "carol@local"},
		},
	}
	if _, err := DeliverAndRelay(accounts, nil, "sender@remote", []string{"alice@local"}, fwdRaw, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, alice); n != 0 {
		t.Errorf("alice inbox = %d, want 0 (Redirect drops the local copy)", n)
	}
	if n := inboxCount(t, carol); n != 1 {
		t.Errorf("carol inbox = %d, want 1 (Redirect delivers to the destination)", n)
	}
}

// TestForwardRedirectExternalRelays: a Redirect to a foreign domain is queued for
// relay, not dropped, and the recipient keeps no local copy.
func TestForwardRedirectExternalRelays(t *testing.T) {
	alice := filepath.Join(t.TempDir(), "alice")
	accounts := forwardingAccounts{
		StaticAccounts: directory.StaticAccounts{"alice@local": {MailboxPath: alice}},
		forwards: map[string]directory.ForwardInfo{
			"alice@local": {Type: directory.ForwardRedirect, Destination: "boss@external.test"},
		},
	}
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	if _, err := DeliverAndRelay(accounts, sp, "sender@remote", []string{"alice@local"}, fwdRaw, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, alice); n != 0 {
		t.Errorf("alice inbox = %d, want 0 (Redirect drops the local copy)", n)
	}
	due, _ := sp.Claim(time.Now(), 10)
	if len(due) != 1 || due[0].Recipient != "boss@external.test" {
		t.Fatalf("spool = %v, want boss@external.test queued for relay", due)
	}
}

// TestForwardRedirectUnreachableNotDropped (advisor #1, no silent drop): a Redirect to
// a destination that can be neither delivered nor relayed (foreign domain, no spool)
// surfaces as unresolved so the caller bounces it — the message must not vanish.
func TestForwardRedirectUnreachableNotDropped(t *testing.T) {
	alice := filepath.Join(t.TempDir(), "alice")
	accounts := forwardingAccounts{
		StaticAccounts: directory.StaticAccounts{"alice@local": {MailboxPath: alice}},
		forwards: map[string]directory.ForwardInfo{
			"alice@local": {Type: directory.ForwardRedirect, Destination: "boss@external.test"},
		},
	}
	unresolved, err := DeliverAndRelay(accounts, nil, "sender@remote", []string{"alice@local"}, fwdRaw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(unresolved, "boss@external.test") {
		t.Errorf("unresolved = %v, want it to contain boss@external.test (no silent drop)", unresolved)
	}
}

// TestForwardToSelfIsNoOp: a forward whose destination is the recipient itself is
// ignored — the message is delivered once locally, with no duplicate or loop.
func TestForwardToSelfIsNoOp(t *testing.T) {
	alice := filepath.Join(t.TempDir(), "alice")
	accounts := forwardingAccounts{
		StaticAccounts: directory.StaticAccounts{"alice@local": {MailboxPath: alice}},
		forwards: map[string]directory.ForwardInfo{
			"alice@local": {Type: directory.ForwardRedirect, Destination: "alice@local"},
		},
	}
	if _, err := DeliverAndRelay(accounts, nil, "sender@remote", []string{"alice@local"}, fwdRaw, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, alice); n != 1 {
		t.Errorf("alice inbox = %d, want 1 (self-forward is a no-op, delivered once)", n)
	}
}

// TestDeliverDoesNotForward: forwarding is applied only on the user-mail path
// (DeliverAndRelay), never on the automated Deliver path (auto-reply, receipt,
// bounce), so an automated message reaches the mailbox itself and is not forwarded.
func TestDeliverDoesNotForward(t *testing.T) {
	alice := filepath.Join(t.TempDir(), "alice")
	carol := filepath.Join(t.TempDir(), "carol")
	accounts := forwardingAccounts{
		StaticAccounts: directory.StaticAccounts{
			"alice@local": {MailboxPath: alice},
			"carol@local": {MailboxPath: carol},
		},
		forwards: map[string]directory.ForwardInfo{
			"alice@local": {Type: directory.ForwardRedirect, Destination: "carol@local"},
		},
	}
	if _, err := Deliver(accounts, "sender@remote", []string{"alice@local"}, fwdRaw, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, alice); n != 1 {
		t.Errorf("alice inbox = %d, want 1 (Deliver does not forward)", n)
	}
	if n := inboxCount(t, carol); n != 0 {
		t.Errorf("carol inbox = %d, want 0 (Deliver does not forward)", n)
	}
}

// TestForwardIsOneHop: a forwarded copy is not itself re-forwarded. alice redirects to
// carol, carol redirects to dave — carol receives it (one hop) and it is NOT relayed
// onward to dave.
func TestForwardIsOneHop(t *testing.T) {
	alice := filepath.Join(t.TempDir(), "alice")
	carol := filepath.Join(t.TempDir(), "carol")
	dave := filepath.Join(t.TempDir(), "dave")
	accounts := forwardingAccounts{
		StaticAccounts: directory.StaticAccounts{
			"alice@local": {MailboxPath: alice},
			"carol@local": {MailboxPath: carol},
			"dave@local":  {MailboxPath: dave},
		},
		forwards: map[string]directory.ForwardInfo{
			"alice@local": {Type: directory.ForwardRedirect, Destination: "carol@local"},
			"carol@local": {Type: directory.ForwardRedirect, Destination: "dave@local"},
		},
	}
	if _, err := DeliverAndRelay(accounts, nil, "sender@remote", []string{"alice@local"}, fwdRaw, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, alice); n != 0 {
		t.Errorf("alice inbox = %d, want 0 (redirected away)", n)
	}
	if n := inboxCount(t, carol); n != 1 {
		t.Errorf("carol inbox = %d, want 1 (one hop lands here)", n)
	}
	if n := inboxCount(t, dave); n != 0 {
		t.Errorf("dave inbox = %d, want 0 (a forwarded copy is not re-forwarded)", n)
	}
}

package mta

import (
	"errors"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

type sentCopyFixtures struct {
	accounts  fakeIdentifier
	sharedDir string
	aliceDir  string
}

// sentCopyWorld provisions alice (the sender), her alias, and a shared mailbox that
// grants her both send rights. cfg is the shared mailbox's sent-copy setting.
func sentCopyWorld(t *testing.T, cfg objectstore.SentCopyConfig) sentCopyFixtures {
	t.Helper()
	aliceDir, sharedDir := t.TempDir(), t.TempDir()
	for _, d := range []string{aliceDir, sharedDir} {
		st, err := objectstore.Open(d)
		if err != nil {
			t.Fatal(err)
		}
		mustClose(t, st)
	}
	st, err := objectstore.Open(sharedDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSendAs([]string{"alice@test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSendOnBehalf([]string{"alice@test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSentCopyConfig(cfg); err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)

	accounts := fakeIdentifier{
		StaticAccounts: directory.StaticAccounts{
			"alice@test":  {MailboxPath: aliceDir},
			"sales@test":  {MailboxPath: aliceDir}, // alice's own alias, same mailbox
			"shared@test": {Shared: true, MailboxPath: sharedDir},
		},
		idents: map[string][]string{"alice@test": {"alice@test", "sales@test"}},
	}
	return sentCopyFixtures{accounts: accounts, sharedDir: sharedDir, aliceDir: aliceDir}
}

// representedMessage builds the wire form of a send in another mailbox's name. A
// send-as send names only that mailbox; an on-behalf send names alice in Sender.
func representedMessage(from string, onBehalf bool) []byte {
	raw := "From: " + from + "\r\n"
	if onBehalf {
		raw += "Sender: alice@test\r\n"
	}
	raw += "To: outside@example.test\r\nSubject: in its name\r\n\r\nbody\r\n"
	return []byte(raw)
}

// sentCount returns how many messages a mailbox's Sent Items holds.
func sentCount(t *testing.T, dir string) int {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// TestTheRepresentedMailboxKeepsACopy is the load-bearing case: a message sent in a
// shared mailbox's name left a record only in the sender's own Sent Items, so nobody
// else with access to that mailbox could see what went out in its name.
func TestTheRepresentedMailboxKeepsACopy(t *testing.T) {
	for _, c := range []struct {
		name     string
		onBehalf bool
	}{
		{"send-as", false},
		{"on-behalf", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := objectstore.SentCopyConfig{ForSendAs: true, ForSendOnBehalf: true}
			fx := sentCopyWorld(t, cfg)

			wantEq(t, "only", fileRepresentedCopy(fx.accounts, "alice@test",
				representedMessage("shared@test", c.onBehalf), time.Now()), false)

			if n := sentCount(t, fx.sharedDir); n != 1 {
				t.Errorf("the represented mailbox holds %d sent messages, want 1", n)
			}
		})
	}
}

// TestTheSettingIsOffByDefault keeps an existing installation behaving as it did: a
// mailbox nobody configured collects nothing.
func TestTheSettingIsOffByDefault(t *testing.T) {
	fx := sentCopyWorld(t, objectstore.SentCopyConfig{})

	for _, onBehalf := range []bool{false, true} {
		wantEq(t, "only", fileRepresentedCopy(fx.accounts, "alice@test",
			representedMessage("shared@test", onBehalf), time.Now()), false)
	}
	if n := sentCount(t, fx.sharedDir); n != 0 {
		t.Errorf("an unconfigured mailbox collected %d copies, want 0", n)
	}
}

// TestEachSettingGatesItsOwnGrant proves the two settings are two answers: a mailbox
// that wants a copy of what is sent AS it need not want one of every on-behalf send.
func TestEachSettingGatesItsOwnGrant(t *testing.T) {
	fx := sentCopyWorld(t, objectstore.SentCopyConfig{ForSendAs: true})

	wantEq(t, "only for unconfigured on-behalf", fileRepresentedCopy(fx.accounts, "alice@test", representedMessage("shared@test", true), time.Now()), false)
	if n := sentCount(t, fx.sharedDir); n != 0 {
		t.Errorf("an on-behalf send was copied although only the send-as setting is on (%d)", n)
	}
	wantEq(t, "only for non-exclusive send-as", fileRepresentedCopy(fx.accounts, "alice@test", representedMessage("shared@test", false), time.Now()), false)
	if n := sentCount(t, fx.sharedDir); n != 1 {
		t.Errorf("a send-as send was not copied although its setting is on (%d)", n)
	}
}

// TestAnAliasSendIsNotARepresentedSend is the case address comparison gets wrong. An
// alias puts a different string in From than the envelope carries, but it is the same
// account, so treating it as a represented send would file a second copy of every
// ordinary aliased message.
func TestAnAliasSendIsNotARepresentedSend(t *testing.T) {
	cfg := objectstore.SentCopyConfig{ForSendAs: true, ForSendOnBehalf: true}
	fx := sentCopyWorld(t, cfg)
	st, err := objectstore.Open(fx.aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSentCopyConfig(cfg); err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)

	wantEq(t, "only for alias send", fileRepresentedCopy(fx.accounts, "alice@test", representedMessage("sales@test", false), time.Now()), false)

	if n := sentCount(t, fx.aliceDir); n != 0 {
		t.Errorf("an alias send filed %d copies into its own mailbox, want 0", n)
	}
}

// TestAnUnreachableMailboxCostsALogLine keeps a completed send completed. The mail
// has already left when the copy is attempted, so a mailbox that will not open must
// cost a log line and no copy, never a failure reported back to the sender.
func TestAnUnreachableMailboxCostsALogLine(t *testing.T) {
	sink := withPassLogger(t)
	accounts := fakeIdentifier{
		StaticAccounts: directory.StaticAccounts{
			"alice@test": {MailboxPath: t.TempDir()},
			"gone@test":  {Shared: true, MailboxPath: "/nonexistent/hermex/mailbox"},
		},
		idents: map[string][]string{"alice@test": {"alice@test"}},
	}
	// It must return, not panic and not block, and never tell the caller to drop the
	// sender's own copy.
	if fileRepresentedCopy(accounts, "alice@test", representedMessage("gone@test", false), time.Now()) {
		t.Error("an unwritten copy was reported as the only one")
	}
	e, ok := sink.find("sent-copy.fail")
	if !ok || e.Fields["stage"] != "open" {
		t.Errorf("expected sent-copy.fail at stage open, got %+v (ok=%v)", e, ok)
	}

	// Forged multi-address From files nothing in shared mailbox.
	fx := sentCopyWorld(t, objectstore.SentCopyConfig{ForSendAs: true, Exclusive: true})
	if fileRepresentedCopy(fx.accounts, "alice@test", representedMessage("shared@test, other@test", false), time.Now()) {
		t.Error("multi-address From reported exclusive copy")
	}
	wantEq(t, "shared Sent Items for multi-address From", sentCount(t, fx.sharedDir), 0)
}

// TestSentCopyCloseFailureLogsAndKeepsOwnCopy proves a store Close error on
// filing a represented Sent copy is logged and prevents Exclusive from dropping
// the sender's own copy.
func TestSentCopyCloseFailureLogsAndKeepsOwnCopy(t *testing.T) {
	fx := sentCopyWorld(t, objectstore.SentCopyConfig{ForSendAs: true, Exclusive: true})
	sink := withPassLogger(t)
	prevClose := closeMailbox
	closeMailbox = func(st *objectstore.Store) error {
		mustClose(t, st)
		return errors.New("sent-copy close error")
	}
	t.Cleanup(func() { closeMailbox = prevClose })

	if fileRepresentedCopy(fx.accounts, "alice@test", representedMessage("shared@test", false), time.Now()) {
		t.Error("failed store Close must not report only=true")
	}
	e, ok := sink.find("sent-copy.fail")
	if !ok || e.Fields["stage"] != "close" || e.Err != "sent-copy close error" {
		t.Errorf("expected sent-copy.fail stage=close, got %+v (ok=%v)", e, ok)
	}
}

// TestExclusiveReportsTheOnlyCopy checks the exclusive setting: once the represented
// mailbox has filed its copy, the caller is told to file none for the sender. Without
// the setting, or when the mailbox took no copy, the sender keeps theirs.
func TestExclusiveReportsTheOnlyCopy(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  objectstore.SentCopyConfig
		only bool
	}{
		{"copy and exclusive", objectstore.SentCopyConfig{ForSendAs: true, Exclusive: true}, true},
		{"copy only", objectstore.SentCopyConfig{ForSendAs: true}, false},
		{"exclusive without copy", objectstore.SentCopyConfig{Exclusive: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := sentCopyWorld(t, c.cfg)
			if got := fileRepresentedCopy(fx.accounts, "alice@test", representedMessage("shared@test", false), time.Now()); got != c.only {
				t.Errorf("only = %v, want %v", got, c.only)
			}
		})
	}
}

// TestSendAndRelayKeepsTheOwnCopyByDefault drives the exported entry: an ordinary
// send keeps the sender's copy, and an exclusive represented send does not.
func TestSendAndRelayKeepsTheOwnCopyByDefault(t *testing.T) {
	fx := sentCopyWorld(t, objectstore.SentCopyConfig{ForSendAs: true, Exclusive: true})
	own := []byte("From: alice@test\r\nTo: alice@test\r\nSubject: own\r\n\r\nbody\r\n")
	unresolved, keep, err := SendAndRelay(fx.accounts, nil, "alice@test", []string{"alice@test"}, own, time.Now())
	if err != nil || !keep || len(unresolved) != 0 {
		t.Errorf("own send: unresolved=%v keep=%v err=%v; want the sender's copy kept", unresolved, keep, err)
	}
	represented := []byte("From: shared@test\r\nTo: alice@test\r\nSubject: in its name\r\n\r\nbody\r\n")
	unresolved, keep, err = SendAndRelay(fx.accounts, nil, "alice@test", []string{"alice@test"}, represented, time.Now())
	if err != nil || keep || len(unresolved) != 0 {
		t.Errorf("exclusive send: unresolved=%v keep=%v err=%v; want no sender copy", unresolved, keep, err)
	}
}

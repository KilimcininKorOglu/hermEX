package mta

import (
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// sentCopyWorld provisions alice (the sender), her alias, and a shared mailbox that
// grants her both send rights. cfg is the shared mailbox's sent-copy setting.
func sentCopyWorld(t *testing.T, cfg objectstore.SentCopyConfig) (accounts fakeIdentifier, sharedDir, aliceDir string) {
	t.Helper()
	aliceDir, sharedDir = t.TempDir(), t.TempDir()
	for _, d := range []string{aliceDir, sharedDir} {
		st, err := objectstore.Open(d)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
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
	st.Close()

	accounts = fakeIdentifier{
		StaticAccounts: directory.StaticAccounts{
			"alice@test":  {MailboxPath: aliceDir},
			"sales@test":  {MailboxPath: aliceDir}, // alice's own alias, same mailbox
			"shared@test": {Shared: true, MailboxPath: sharedDir},
		},
		idents: map[string][]string{"alice@test": {"alice@test", "sales@test"}},
	}
	return accounts, sharedDir, aliceDir
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
	defer st.Close()
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
			accounts, sharedDir, _ := sentCopyWorld(t, cfg)

			fileRepresentedCopy(accounts, "alice@test",
				representedMessage("shared@test", c.onBehalf), time.Now())

			if n := sentCount(t, sharedDir); n != 1 {
				t.Errorf("the represented mailbox holds %d sent messages, want 1", n)
			}
		})
	}
}

// TestTheSettingIsOffByDefault keeps an existing installation behaving as it did: a
// mailbox nobody configured collects nothing.
func TestTheSettingIsOffByDefault(t *testing.T) {
	accounts, sharedDir, _ := sentCopyWorld(t, objectstore.SentCopyConfig{})

	for _, onBehalf := range []bool{false, true} {
		fileRepresentedCopy(accounts, "alice@test",
			representedMessage("shared@test", onBehalf), time.Now())
	}
	if n := sentCount(t, sharedDir); n != 0 {
		t.Errorf("an unconfigured mailbox collected %d copies, want 0", n)
	}
}

// TestEachSettingGatesItsOwnGrant proves the two settings are two answers: a mailbox
// that wants a copy of what is sent AS it need not want one of every on-behalf send.
func TestEachSettingGatesItsOwnGrant(t *testing.T) {
	accounts, sharedDir, _ := sentCopyWorld(t, objectstore.SentCopyConfig{ForSendAs: true})

	fileRepresentedCopy(accounts, "alice@test", representedMessage("shared@test", true), time.Now())
	if n := sentCount(t, sharedDir); n != 0 {
		t.Errorf("an on-behalf send was copied although only the send-as setting is on (%d)", n)
	}
	fileRepresentedCopy(accounts, "alice@test", representedMessage("shared@test", false), time.Now())
	if n := sentCount(t, sharedDir); n != 1 {
		t.Errorf("a send-as send was not copied although its setting is on (%d)", n)
	}
}

// TestAnAliasSendIsNotARepresentedSend is the case address comparison gets wrong. An
// alias puts a different string in From than the envelope carries, but it is the same
// account, so treating it as a represented send would file a second copy of every
// ordinary aliased message.
func TestAnAliasSendIsNotARepresentedSend(t *testing.T) {
	cfg := objectstore.SentCopyConfig{ForSendAs: true, ForSendOnBehalf: true}
	accounts, _, aliceDir := sentCopyWorld(t, cfg)
	st, err := objectstore.Open(aliceDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSentCopyConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st.Close()

	fileRepresentedCopy(accounts, "alice@test", representedMessage("sales@test", false), time.Now())

	if n := sentCount(t, aliceDir); n != 0 {
		t.Errorf("an alias send filed %d copies into its own mailbox, want 0", n)
	}
}

// TestAnUnreachableMailboxCostsALogLine keeps a completed send completed. The mail
// has already left when the copy is attempted, so a mailbox that will not open must
// cost a log line and no copy, never a failure reported back to the sender.
func TestAnUnreachableMailboxCostsALogLine(t *testing.T) {
	accounts := fakeIdentifier{
		StaticAccounts: directory.StaticAccounts{
			"alice@test": {MailboxPath: t.TempDir()},
			"gone@test":  {Shared: true, MailboxPath: "/nonexistent/hermex/mailbox"},
		},
		idents: map[string][]string{"alice@test": {"alice@test"}},
	}
	// It must return, not panic and not block.
	fileRepresentedCopy(accounts, "alice@test", representedMessage("gone@test", false), time.Now())
}

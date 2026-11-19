package fetchmail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/lifecycle"
)

// TestAPanickingAccountFailsOnlyItself polls two POP3 accounts and makes delivery
// into the first one panic. Fetchmail runs as a background loop with nothing above
// it, so an unrecovered panic would end the daemon and, since the message is still
// on the source, end it again on every start. The poll must report the first
// account's failure and still deliver the second account's mail.
func TestAPanickingAccountFailsOnlyItself(t *testing.T) {
	allowLoopback(t)
	poisonHost, poisonPort, _ := fakePOP3(t, []string{"From: a\r\n\r\npoison"})
	okHost, okPort, _ := fakePOP3(t, []string{"From: b\r\n\r\nfine"})
	store := &fakeWorkerStore{configs: []directory.FetchmailEntry{
		{ID: 1, Mailbox: "carol@hermex.test", Active: true, SrcServer: poisonHost, SrcPort: poisonPort,
			SrcUser: "carol", SrcPassword: "pw", Protocol: "POP3"},
		{ID: 2, Mailbox: "alice@hermex.test", Active: true, SrcServer: okHost, SrcPort: okPort,
			SrcUser: "alice", SrcPassword: "pw", Protocol: "POP3"},
	}}
	var got []string
	deliver := func(mailbox string, raw []byte, _ time.Time) error {
		if mailbox == "carol@hermex.test" {
			panic("import bug")
		}
		got = append(got, string(raw))
		return nil
	}

	n, errs := Poll(context.Background(), store, deliver, pollTime)
	if n != 1 || len(got) != 1 || !strings.Contains(got[0], "fine") {
		t.Errorf("delivered %d %q, want the healthy account's message", n, got)
	}
	if len(errs) != 1 || !errors.Is(errs[0], lifecycle.ErrPanicked) || !strings.Contains(errs[0].Error(), "carol@") {
		t.Errorf("errors = %v, want one contained panic naming the failing account", errs)
	}
}

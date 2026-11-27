package imap

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestExpungeThatTheStoreRefusesKeepsTheMessage selects two \Deleted messages
// on a store that refuses every write. RFC 3501 7.4.1: an untagged EXPUNGE says
// the message is gone and the numbers after it shift down. A message the store
// did not remove must therefore get no EXPUNGE and keep its slot, or the client
// and the server number the mailbox differently from then on; the command says
// NO, and the failure is recorded.
func TestExpungeThatTheStoreRefusesKeepsTheMessage(t *testing.T) {
	sink := &captureSink{}
	var out bytes.Buffer
	c := &conn{srv: &Server{Logger: logging.New(sink)}, user: "bob@hermex.test", st: closedStore(t), bw: bufio.NewWriter(&out)}
	c.state = stateSelected
	c.sel = &selectedMailbox{
		id: mapi.PrivateFIDInbox,
		msgs: []objectstore.MessageInfo{
			{UID: 7, Flags: objectstore.FlagDeleted},
			{UID: 8, Flags: objectstore.FlagDeleted},
		},
	}

	c.cmdExpunge("a")
	c.flush()

	if strings.Contains(out.String(), "EXPUNGE\r\n") && strings.Contains(out.String(), "* ") {
		t.Errorf("an EXPUNGE was sent for a message the store kept: %q", out.String())
	}
	if !strings.HasPrefix(out.String(), "a NO") {
		t.Errorf("reply = %q, want a tagged NO", out.String())
	}
	if len(c.sel.msgs) != 2 {
		t.Errorf("the session dropped %d message(s) the store still holds", 2-len(c.sel.msgs))
	}
	if findEvent(sink.snapshot(), "expunge.fail") == nil {
		t.Error("the refused expunge left no record")
	}
}

// TestCopyThatFailsPartwayLeavesTheDestinationAsItWas copies two messages where
// the second cannot be read. RFC 3501 6.4.7: when COPY fails for any reason, the
// destination mailbox is restored to its state before the attempt, so the first
// copy must not stay behind.
func TestCopyThatFailsPartwayLeavesTheDestinationAsItWas(t *testing.T) {
	ts := startServer(t)
	st, err := objectstore.Open(ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	dest, err := st.CreateFolder(nil, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	c := &conn{srv: &Server{Logger: logging.New(&captureSink{})}, st: st}
	c.sel = &selectedMailbox{id: mapi.PrivateFIDInbox, msgs: []objectstore.MessageInfo{{UID: 1}, {UID: 99}}}

	all, err := parseSeqSet("1:*")
	mustNoErr(t, "parse 1:*", err)
	if src, dst, ok := c.copyToDest(all, true, st, dest); ok {
		t.Fatalf("copy of a message that cannot be read reported success (src=%v, dst=%v)", src, dst)
	}
	left, err := st.ListMessages(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("the failed COPY left %d message(s) in the destination, want none", len(left))
	}
}

// TestMoveWhoseSourceDeleteFailsKeepsTheMessage covers the last step of MOVE
// (RFC 6851 3.3): after the copy, each source message is removed with an
// EXPUNGE. A source message the store refused to remove must keep its slot and
// get no EXPUNGE (RFC 3501 7.4.1), and the failure is counted and recorded.
func TestMoveWhoseSourceDeleteFailsKeepsTheMessage(t *testing.T) {
	sink := &captureSink{}
	var out bytes.Buffer
	c := &conn{srv: &Server{Logger: logging.New(sink)}, user: "bob@hermex.test", st: closedStore(t), bw: bufio.NewWriter(&out)}
	c.sel = &selectedMailbox{id: mapi.PrivateFIDInbox, msgs: []objectstore.MessageInfo{{UID: 7}, {UID: 8}}}

	failed := c.expungeMoved([]uint32{7})
	c.flush()

	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if strings.Contains(out.String(), "EXPUNGE") {
		t.Errorf("an EXPUNGE was sent for a message the store kept: %q", out.String())
	}
	if len(c.sel.msgs) != 2 {
		t.Errorf("the session holds %d messages, want both", len(c.sel.msgs))
	}
	if findEvent(sink.snapshot(), "expunge.fail") == nil {
		t.Error("the refused removal left no record")
	}
}

// TestIMAPExpungeGoesToDumpster proves an IMAP EXPUNGE soft-deletes the \Deleted
// message into the Recoverable Items dumpster rather than purging it: it leaves the
// mailbox but stays recoverable. This is the IMAP leg of routing every hard-delete
// into the dumpster.
func TestIMAPExpungeGoesToDumpster(t *testing.T) {
	ts := startServer(t)
	c := ts.client
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")
	c.mustOK("a3", `STORE 1 +FLAGS (\Deleted)`)
	c.mustOK("a4", "EXPUNGE")

	// A second handle (WAL allows concurrent reads) sees the expunged message in the
	// dumpster, not gone.
	st, err := objectstore.Open(ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	dump, err := st.ListSoftDeleted(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(dump) != 1 {
		t.Fatalf("dumpster has %d items after EXPUNGE, want 1 (recoverable)", len(dump))
	}
}

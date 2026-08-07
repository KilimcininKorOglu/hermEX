package imap

import (
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// closedStore returns a store whose database is shut, so every write fails the way
// a real store failure does rather than through a stub the production path never
// sees.
func closedStore(t *testing.T) *objectstore.Store {
	t.Helper()
	st, err := objectstore.Open(filepath.Join(t.TempDir(), "mbox"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return st
}

// seenConn builds a connection with one unread message selected.
func seenConn(sink logging.Sink) *conn {
	c := &conn{srv: &Server{Logger: logging.New(sink)}, user: "bob@hermex.test"}
	c.sel = &selectedMailbox{
		id:   mapi.PrivateFIDInbox,
		msgs: []objectstore.MessageInfo{{UID: 7}},
	}
	return c
}

// TestImplicitSeenFailureIsRecorded is the defect. A FETCH BODY[] sets \Seen as a
// side effect. The store write was issued and its error dropped, then the cached
// row and the FLAGS sent to the client were updated regardless. A failed write left
// the session claiming a flag the store never took: the client stops showing the
// message as unread, the next session shows it unread again, and nothing anywhere
// says why.
func TestImplicitSeenFailureIsRecorded(t *testing.T) {
	sink := &captureSink{}
	c := seenConn(sink)

	c.markSeen(&c.sel.msgs[0], closedStore(t))

	e, ok := find(sink.snapshot(), "fetch.seen.fail")
	if !ok {
		t.Fatal("a failed \\Seen write left no record, so nothing says the flag was lost")
	}
	if e.Level != logging.LevelError || e.Subsystem != logging.IMAP {
		t.Errorf("event = %s/%s, want error/imap", e.Level, e.Subsystem)
	}
	if e.Fields["uid"] != uint32(7) {
		t.Errorf("uid = %v, want 7", e.Fields["uid"])
	}
	if e.Fields["error"] == nil || e.Fields["error"] == "" {
		t.Error("the record carries no cause")
	}
}

// TestImplicitSeenFailureLeavesTheRowUnread proves the session stops lying about it.
// Recording the failure is only half the fix: the cached row and the FLAGS sent to
// the client must keep saying unread, or the client hides a message the store still
// holds as unread.
func TestImplicitSeenFailureLeavesTheRowUnread(t *testing.T) {
	c := seenConn(&captureSink{})

	if applied := c.markSeen(&c.sel.msgs[0], closedStore(t)); applied {
		t.Error("markSeen reported the flag applied after the store refused the write")
	}
	if c.sel.msgs[0].Flags&objectstore.FlagSeen != 0 {
		t.Error("the cached row was marked seen even though the store refused the write")
	}
}

// TestImplicitSeenSuccessIsQuiet guards the other direction. The ordinary path must
// apply the flag and record nothing, or the event becomes noise on every read and an
// operator learns to skip it.
func TestImplicitSeenSuccessIsQuiet(t *testing.T) {
	sink := &captureSink{}
	st, err := objectstore.Open(filepath.Join(t.TempDir(), "mbox"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	info, err := st.AppendMessage(mapi.PrivateFIDInbox, []byte("Subject: one\r\n\r\nbody"), time.Unix(1, 0), 0)
	if err != nil {
		t.Fatal(err)
	}

	c := seenConn(sink)
	c.sel.msgs[0].UID = info.UID
	if applied := c.markSeen(&c.sel.msgs[0], st); !applied {
		t.Fatal("markSeen did not apply the flag on the ordinary path")
	}
	if c.sel.msgs[0].Flags&objectstore.FlagSeen == 0 {
		t.Error("the cached row was not marked seen")
	}
	if e, ok := find(sink.snapshot(), "fetch.seen.fail"); ok {
		t.Errorf("a successful write was recorded as failed: %v", e.Fields)
	}
}

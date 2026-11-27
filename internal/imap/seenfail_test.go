package imap

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
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
	mustClose(t, st)
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

	e := findEvent(sink.snapshot(), "fetch.seen.fail")
	if e == nil {
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

// TestGetQuotaOnAnUnreadableStoreFails proves a quota that cannot be read is not
// reported as unlimited. Reporting "QUOTA () " for a store whose limits failed to
// load tells the client it has no limit while delivery still enforces one, so the
// command must fail and the cause must be recorded.
func TestGetQuotaOnAnUnreadableStoreFails(t *testing.T) {
	sink := &captureSink{}
	var out bytes.Buffer
	c := &conn{srv: &Server{Logger: logging.New(sink)}, user: "bob@hermex.test", st: closedStore(t), bw: bufio.NewWriter(&out), state: stateAuth}

	c.cmdGetQuota("q", []token{{val: ""}})
	c.flush()

	if strings.Contains(out.String(), "* QUOTA") {
		t.Errorf("a quota that could not be read was reported: %q", out.String())
	}
	if !strings.HasPrefix(out.String(), "q NO") {
		t.Errorf("reply = %q, want a tagged NO", out.String())
	}
	if findEvent(sink.snapshot(), "quota.read.fail") == nil {
		t.Error("the failed quota read left no record")
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
	defer mustClose(t, st)
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
	if e := findEvent(sink.snapshot(), "fetch.seen.fail"); e != nil {
		t.Errorf("a successful write was recorded as failed: %v", e.Fields)
	}
}

// TestCondstoreUnreadableStoreRecordsAndFailsClosed proves Rule 5 CONDSTORE error
// paths fail closed and log instead of swallowing store errors.
func TestCondstoreUnreadableStoreRecordsAndFailsClosed(t *testing.T) {
	sink := &captureSink{}
	var out bytes.Buffer
	st := closedStore(t)
	c := &conn{
		srv:       &Server{Logger: logging.New(sink)},
		user:      "bob@hermex.test",
		st:        st,
		bw:        bufio.NewWriter(&out),
		state:     stateSelected,
		condstore: true,
		sel: &selectedMailbox{
			id:          mapi.PrivateFIDInbox,
			uidValidity: 1,
			uidNext:     2,
			msgs:        []objectstore.MessageInfo{{UID: 7}},
		},
	}

	if m, err := c.modseqMap(); err == nil || m != nil {
		t.Errorf("modseqMap on closed store = (%v, %v), want error", m, err)
	}
	if findEvent(sink.snapshot(), "condstore.modseq.fail") == nil {
		t.Error("expected condstore.modseq.fail event")
	}

	out.Reset()
	c.cmdStore("s1", []token{
		{kind: tAtom, val: "1"},
		{kind: tLParen}, {kind: tAtom, val: "UNCHANGEDSINCE"}, {kind: tAtom, val: "1"}, {kind: tRParen},
		{kind: tAtom, val: "+FLAGS"},
		{kind: tAtom, val: `\Flagged`},
	}, false)
	c.flush()
	wantPrefix(t, "conditional STORE on closed store", out.String(), "s1 NO")
	wantEq(t, "flags after refused conditional STORE", c.sel.msgs[0].Flags, int64(0))

	out.Reset()
	c.qrUIDValidity = 1
	c.qrActive = true
	c.emitSelected("sel", c.sel, false)
	c.flush()
	wantContains(t, "SELECT on closed modseq store", out.String(), "[NOMODSEQ]")
	if findEvent(sink.snapshot(), "condstore.highestmodseq.fail") == nil {
		t.Error("expected condstore.highestmodseq.fail event")
	}
	if findEvent(sink.snapshot(), "condstore.vanished.fail") == nil {
		t.Error("expected condstore.vanished.fail event")
	}
}

// TestFetchAndSearchReadFailuresAreRecorded proves message-read and envelope-parse
// errors in FETCH and SEARCH are recorded instead of silently dropped.
func TestFetchAndSearchReadFailuresAreRecorded(t *testing.T) {
	sink := &captureSink{}
	st := closedStore(t)
	c := &conn{
		srv:   &Server{Logger: logging.New(sink)},
		user:  "bob@hermex.test",
		st:    st,
		state: stateSelected,
		sel:   &selectedMailbox{id: mapi.PrivateFIDInbox, msgs: []objectstore.MessageInfo{{UID: 7}}},
	}

	r := &fetchRenderer{c: c, msg: c.sel.msgs[0]}
	if env := r.renderEnvelopeField(); env != "ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)" {
		t.Errorf("renderEnvelopeField on unreadable message = %q, want all-NIL envelope", env)
	}
	if findEvent(sink.snapshot(), "fetch.read.fail") == nil {
		t.Error("expected fetch.read.fail event")
	}
	if findEvent(sink.snapshot(), "fetch.envelope.fail") == nil {
		t.Error("expected fetch.envelope.fail event")
	}

	sc := &searchCtx{seq: 1, msg: c.sel.msgs[0], c: c}
	sc.load()
	if findEvent(sink.snapshot(), "search.read.fail") == nil {
		t.Error("expected search.read.fail event")
	}
}

// TestStatusAndUIDPlusReadFailuresAreRecorded proves readStatusMeta and
// destUIDValidity record store errors instead of emitting zero values.
func TestStatusAndUIDPlusReadFailuresAreRecorded(t *testing.T) {
	sink := &captureSink{}
	st := closedStore(t)
	c := &conn{srv: &Server{Logger: logging.New(sink)}, user: "bob@hermex.test", st: st}

	if meta, err := c.readStatusMeta(st, mapi.PrivateFIDInbox); err == nil {
		t.Errorf("readStatusMeta on closed store = (%+v, nil), want error", meta)
	}
	if findEvent(sink.snapshot(), "status.read.fail") == nil {
		t.Error("expected status.read.fail event")
	}

	if uidv, ok := c.destUIDValidity(st, mapi.PrivateFIDInbox); ok || uidv != 0 {
		t.Errorf("destUIDValidity on closed store = (%d, %v), want (0, false)", uidv, ok)
	}
	if findEvent(sink.snapshot(), "uidplus.uidvalidity.fail") == nil {
		t.Error("expected uidplus.uidvalidity.fail event")
	}
}

// errCloseConn simulates a client connection whose Close returns a non-closed error.
type errCloseConn struct {
	net.Conn
	closeErr error
}

func (e errCloseConn) Read([]byte) (int, error)    { return 0, errors.New("eof") }
func (e errCloseConn) Write(p []byte) (int, error) { return len(p), nil }
func (e errCloseConn) Close() error                { return e.closeErr }
func (e errCloseConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 143}
}
func (e errCloseConn) LocalAddr() net.Addr              { return e.RemoteAddr() }
func (e errCloseConn) SetDeadline(time.Time) error      { return nil }
func (e errCloseConn) SetReadDeadline(time.Time) error  { return nil }
func (e errCloseConn) SetWriteDeadline(time.Time) error { return nil }

// TestCloseFailuresAreRecorded proves conn.Close errors during session teardown
// are logged unless they are net.ErrClosed.
func TestCloseFailuresAreRecorded(t *testing.T) {
	sink := &captureSink{}
	srv := &Server{Auth: directory.StaticAccounts{}, Hostname: "mail.test", Logger: logging.New(sink)}
	srv.handle(errCloseConn{closeErr: errors.New("reset by peer")})

	if findEvent(sink.snapshot(), "conn.close.fail") == nil {
		t.Error("expected conn.close.fail event")
	}

	sinkQuiet := &captureSink{}
	c := &conn{srv: &Server{Logger: logging.New(sinkQuiet)}}
	c.recordCloseErr(logging.LevelWarn, "conn.close.fail", net.ErrClosed)
	if findEvent(sinkQuiet.snapshot(), "conn.close.fail") != nil {
		t.Error("net.ErrClosed should not emit conn.close.fail")
	}
}

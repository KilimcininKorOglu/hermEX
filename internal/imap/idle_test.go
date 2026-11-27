package imap

import (
	"bufio"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestIdleEndIsBoundedAndMustBeDone reads the line that ends an IDLE. The client
// has authenticated but is still hostile input: the line is read under the same
// command-line cap as every other (here 128 bytes), so an endless line cannot grow
// the server's memory, and RFC 2177 section 3 ends IDLE with DONE only, so any
// other line is a protocol error. DONE ends it in any case, with or without CR.
func TestIdleEndIsBoundedAndMustBeDone(t *testing.T) {
	var line atomic.Int64
	line.Store(128)
	for _, c := range []struct {
		in   string
		want error
	}{
		{"DONE\r\n", nil},
		{"done\n", nil},
		{strings.Repeat("x", 1<<20) + "\r\n", errLineTooLong},
		{"a NOOP\r\n", errProtocol},
	} {
		cn := &conn{rd: &commandReader{br: bufio.NewReader(strings.NewReader(c.in)), maxLine: &line}}
		err := cn.readIdleEnd()
		if (c.want == nil) != (err == nil) || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("readIdleEnd(%.20q) = %v, want %v", c.in, err, c.want)
		}
	}
}

// fakeIdleWaker is a controllable wake source: Register hands back a channel the
// test fires to simulate a push event for the idling mailbox.
type fakeIdleWaker struct{ ch chan struct{} }

func (f *fakeIdleWaker) Register(string) (<-chan struct{}, func()) { return f.ch, func() {} }
func (f *fakeIdleWaker) fire() {
	select {
	case f.ch <- struct{}{}:
	default:
	}
}

// TestIMAPIdle proves the IDLE command end-to-end (RFC 2177): the server
// acknowledges with a continuation, pushes an untagged EXISTS when a delivery lands
// during IDLE (woken by the push relay, well before the 30s poll cadence), ends the
// command with a tagged OK on DONE, and, critically, keeps parsing commands on the
// same reader afterward, proving the DONE-reader goroutine left the stream resumable.
func TestIMAPIdle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	inbox := int64(mapi.PrivateFIDInbox)
	appendTo(t, path, inbox, "Subject: one\r\n\r\nbody", time.Unix(1, 0))

	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	srv := &Server{Auth: auth, Hostname: "mail.test"}
	waker := &fakeIdleWaker{ch: make(chan struct{}, 1)}
	srv.waker = waker
	ln := serveOn(t, srv)

	conn, err := net.Dial("tcp", ln.Addr().String())
	mustNoErr(t, "dial", err)
	t.Cleanup(func() { mustClose(t, conn) })
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")

	c.mustOK("a1", "LOGIN alice secret")
	// RFC 2177: IDLE requires a selected mailbox.
	wantStatus(t, "IDLE before SELECT", c, "a1i", "IDLE", "BAD")
	c.mustOK("a2", "SELECT INBOX")

	// IDLE must be advertised in CAPABILITY (RFC 2177 §3).
	caps := c.mustOK("a3", "CAPABILITY")
	if len(caps) == 0 {
		t.Fatal("CAPABILITY returned nothing")
	}
	wantContains(t, "CAPABILITY", caps[0], "IDLE")

	// Begin IDLE: the server requests a continuation.
	mustSend(c.t, c.conn, "a4 IDLE\r\n")
	wantPrefix(t, "the IDLE continuation", c.line(), "+")

	// A delivery during IDLE, through a separate store handle (a different daemon's
	// MTA), plus the push wake: the untagged EXISTS must arrive without the cadence.
	appendTo(t, path, inbox, "Subject: new\r\n\r\nbody", time.Unix(3, 0))
	waker.fire()

	// Bound the wait: the cadence is 30s.
	mustNoErr(t, "set the read deadline", conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	for !strings.Contains(c.line(), "EXISTS") {
	}
	mustNoErr(t, "clear the read deadline", conn.SetReadDeadline(time.Time{}))

	// End IDLE with DONE; the server sends the tagged completion.
	mustSend(c.t, c.conn, "DONE\r\n")
	wantEq(t, "the IDLE termination status", c.collect("a4").status, "OK")

	// A non-DONE line during IDLE ends it with BAD and keeps the connection usable.
	mustSend(c.t, c.conn, "a4b IDLE\r\n")
	wantPrefix(t, "the second IDLE continuation", c.line(), "+")
	mustSend(c.t, c.conn, "NOTDONE\r\n")
	wantEq(t, "non-DONE IDLE termination status", c.collect("a4b").status, "BAD")

	// The reader resumed cleanly: a normal command after IDLE still parses.
	c.mustOK("a5", "NOOP")
}

// appendTo stores one message in a mailbox through its own store handle, which is
// how another daemon's delivery reaches a mailbox an IMAP session holds open.
func appendTo(t *testing.T, path string, folderID int64, raw string, when time.Time) {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open the mailbox", err)
	defer mustClose(t, st)
	mustAppend(t, st, folderID, raw, when, 0)
}

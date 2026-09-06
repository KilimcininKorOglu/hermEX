package imap

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, "listen", err)
	t.Cleanup(func() { _ = ln.Close() })
	srv := &Server{Auth: auth, Hostname: "mail.test"}
	waker := &fakeIdleWaker{ch: make(chan struct{}, 1)}
	srv.waker = waker
	go func() { _ = srv.Serve(ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	mustNoErr(t, "dial", err)
	t.Cleanup(func() { _ = conn.Close() })
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")

	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")

	// IDLE must be advertised in CAPABILITY (RFC 2177 §3).
	caps := c.mustOK("a3", "CAPABILITY")
	if len(caps) == 0 {
		t.Fatal("CAPABILITY returned nothing")
	}
	wantContains(t, "CAPABILITY", caps[0], "IDLE")

	// Begin IDLE: the server requests a continuation.
	_, _ = fmt.Fprintf(c.conn, "a4 IDLE\r\n")
	wantPrefix(t, "the IDLE continuation", c.line(), "+")

	// A delivery during IDLE, through a separate store handle (a different daemon's
	// MTA), plus the push wake: the untagged EXISTS must arrive without the cadence.
	appendTo(t, path, inbox, "Subject: new\r\n\r\nbody", time.Unix(3, 0))
	waker.fire()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second)) // bound the wait: cadence is 30s
	for !strings.Contains(c.line(), "EXISTS") {
	}
	_ = conn.SetReadDeadline(time.Time{}) // clear

	// End IDLE with DONE; the server sends the tagged completion.
	_, _ = fmt.Fprintf(c.conn, "DONE\r\n")
	_, status := c.collect("a4")
	wantEq(t, "the IDLE termination status", status, "OK")

	// The reader resumed cleanly: a normal command after IDLE still parses.
	c.mustOK("a5", "NOOP")
}

// appendTo stores one message in a mailbox through its own store handle, which is
// how another daemon's delivery reaches a mailbox an IMAP session holds open.
func appendTo(t *testing.T, path string, folderID int64, raw string, when time.Time) {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open the mailbox", err)
	defer st.Close()
	_, err = st.AppendMessage(folderID, []byte(raw), when, 0)
	mustNoErr(t, "append the message", err)
}

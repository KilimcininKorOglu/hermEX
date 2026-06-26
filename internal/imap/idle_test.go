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
// command with a tagged OK on DONE, and — critically — keeps parsing commands on the
// same reader afterward, proving the DONE-reader goroutine left the stream resumable.
func TestIMAPIdle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
	if _, err := st.AppendMessage(inbox, []byte("Subject: one\r\n\r\nbody"), time.Unix(1, 0), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	srv := &Server{Auth: auth, Hostname: "mail.test"}
	waker := &fakeIdleWaker{ch: make(chan struct{}, 1)}
	srv.waker = waker
	go srv.Serve(ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")

	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")

	// IDLE must be advertised in CAPABILITY (RFC 2177 §3).
	caps := c.mustOK("a3", "CAPABILITY")
	if len(caps) == 0 || !strings.Contains(caps[0], "IDLE") {
		t.Errorf("CAPABILITY does not advertise IDLE: %v", caps)
	}

	// Begin IDLE: the server requests a continuation.
	fmt.Fprintf(c.conn, "a4 IDLE\r\n")
	if cont := c.line(); !strings.HasPrefix(cont, "+") {
		t.Fatalf("IDLE continuation = %q, want a + line", cont)
	}

	// A delivery during IDLE, through a separate store handle (a different daemon's
	// MTA), plus the push wake: the untagged EXISTS must arrive without the cadence.
	st2, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st2.AppendMessage(inbox, []byte("Subject: new\r\n\r\nbody"), time.Unix(3, 0), 0); err != nil {
		t.Fatal(err)
	}
	st2.Close()
	waker.fire()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second)) // bound the wait: cadence is 30s
	gotExists := false
	for !gotExists {
		l := c.line()
		if strings.Contains(l, "EXISTS") {
			gotExists = true
		}
	}
	conn.SetReadDeadline(time.Time{}) // clear
	if !gotExists {
		t.Fatal("no untagged EXISTS arrived during IDLE")
	}

	// End IDLE with DONE; the server sends the tagged completion.
	fmt.Fprintf(c.conn, "DONE\r\n")
	if _, status := c.collect("a4"); status != "OK" {
		t.Errorf("IDLE terminated status = %s, want OK", status)
	}

	// The reader resumed cleanly: a normal command after IDLE still parses.
	c.mustOK("a5", "NOOP")
}

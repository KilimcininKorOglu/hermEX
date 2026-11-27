package imap

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"

	"hermex/internal/connlimit"
	"hermex/internal/directory"
)

type greetedConn struct {
	conn net.Conn
	line string
}

// cappedServer serves IMAP with a connection cap of one per client address, and
// returns the listener.
func cappedServer(t *testing.T) net.Listener {
	t.Helper()
	limiter := connlimit.New()
	limiter.SetLimits(100, 1)
	limiter.SetEnabled(true)
	srv := &Server{Auth: directory.StaticAccounts{}, Hostname: "mail.test"}
	srv.SetConnLimiter(limiter)
	return serveOn(t, srv)
}

// firstLine dials the listener and returns the first line the server sends.
func firstLine(t *testing.T, ln net.Listener) greetedConn {
	t.Helper()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read greeting: %v", errors.Join(err, conn.Close()))
	}
	return greetedConn{conn: conn, line: strings.TrimRight(line, "\r\n")}
}

// TestConnCapRefusesWithBye is the load-bearing case: a client past the cap is told
// why in the greeting RFC 3501 reserves for a server that will not serve the
// connection, rather than seeing a bare disconnect.
func TestConnCapRefusesWithBye(t *testing.T) {
	ln := cappedServer(t)

	first := firstLine(t, ln)
	defer mustClose(t, first.conn)
	if !strings.HasPrefix(first.line, "* OK") {
		t.Fatalf("first connection greeting = %q, want an OK greeting", first.line)
	}

	second := firstLine(t, ln)
	defer mustClose(t, second.conn)
	if !strings.HasPrefix(second.line, "* BYE") {
		t.Errorf("second connection greeting = %q, want an untagged BYE", second.line)
	}
	if !strings.Contains(second.line, "too many connections") {
		t.Errorf("refusal = %q, want it to name the cap", second.line)
	}

	// Refusing the second connection leaves the first session alive.
	mustSend(t, first.conn, "a1 NOOP\r\n")
	resp, err := bufio.NewReader(first.conn).ReadString('\n')
	mustNoErr(t, "read NOOP on first conn", err)
	wantPrefix(t, "NOOP on first conn", resp, "a1 OK")
}

// TestConnCapAdmitsAfterAClose proves the cap is a concurrency bound, not a
// lifetime one: closing the first connection frees the slot.
func TestConnCapAdmitsAfterAClose(t *testing.T) {
	ln := cappedServer(t)

	first := firstLine(t, ln)
	wantPrefix(t, "first connection greeting", first.line, "* OK")
	mustClose(t, first.conn)

	// The release runs when the handler returns, which happens once the server
	// notices the close, so retry briefly rather than racing it.
	var greeting string
	for range 100 {
		gc := firstLine(t, ln)
		greeting = gc.line
		mustClose(t, gc.conn)
		if strings.HasPrefix(greeting, "* OK") {
			return
		}
	}
	t.Errorf("after the first connection closed, a new one still got %q", greeting)
}

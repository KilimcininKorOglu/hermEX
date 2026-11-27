package pop3

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"

	"hermex/internal/connlimit"
	"hermex/internal/directory"
)

// cappedPOP3 serves POP3 with a connection cap of one per client address.
func cappedPOP3(t *testing.T) net.Listener {
	t.Helper()
	limiter := connlimit.New()
	limiter.SetLimits(100, 1)
	limiter.SetEnabled(true)
	srv := &Server{Auth: directory.StaticAccounts{}, Hostname: "mail.test"}
	srv.SetConnLimiter(limiter)
	return serveOn(t, srv)
}

// greetingOf dials the listener and returns the connection, a reader on it and
// the first line the server sends.
func greetingOf(t *testing.T, ln net.Listener) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read greeting: %v", errors.Join(err, conn.Close()))
	}
	return conn, br, strings.TrimRight(line, "\r\n")
}

// TestConnCapRefusesWithErr proves a client past the cap gets the -ERR greeting
// RFC 1939 defines for a server that will not serve the session, not a bare
// disconnect, and that the refusal leaves the admitted session serving.
func TestConnCapRefusesWithErr(t *testing.T) {
	ln := cappedPOP3(t)

	first, firstR, greeting := greetingOf(t, ln)
	defer mustClose(t, first)
	if !strings.HasPrefix(greeting, "+OK") {
		t.Fatalf("first connection greeting = %q, want +OK", greeting)
	}

	second, _, refusal := greetingOf(t, ln)
	defer mustClose(t, second)
	if !strings.HasPrefix(refusal, "-ERR") || !strings.Contains(refusal, "too many connections") {
		t.Errorf("second connection greeting = %q, want an -ERR naming the cap", refusal)
	}

	// The refusal must not disturb the session the cap admitted.
	mustSend(t, first, "CAPA\r\n")
	if l, err := firstR.ReadString('\n'); err != nil || !strings.HasPrefix(l, "+OK") {
		t.Errorf("CAPA on the admitted session after the refusal = %q (err %v), want +OK", l, err)
	}
}

package pop3

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"hermex/internal/connlimit"
	"hermex/internal/directory"
)

// cappedPOP3 serves POP3 with a connection cap of one per client address.
func cappedPOP3(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	limiter := connlimit.New()
	limiter.SetLimits(100, 1)
	limiter.SetEnabled(true)
	srv := &Server{Auth: directory.StaticAccounts{}, Hostname: "mail.test"}
	srv.SetConnLimiter(limiter)
	go func() { _ = srv.Serve(ln) }()
	return ln
}

// greetingOf dials the listener and returns the first line the server sends.
func greetingOf(t *testing.T, ln net.Listener) (net.Conn, string) {
	t.Helper()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		_ = conn.Close()
		t.Fatalf("read greeting: %v", err)
	}
	return conn, strings.TrimRight(line, "\r\n")
}

// TestConnCapRefusesWithErr proves a client past the cap gets the -ERR greeting
// RFC 1939 defines for a server that will not serve the session, not a bare
// disconnect.
func TestConnCapRefusesWithErr(t *testing.T) {
	ln := cappedPOP3(t)

	first, greeting := greetingOf(t, ln)
	defer first.Close()
	if !strings.HasPrefix(greeting, "+OK") {
		t.Fatalf("first connection greeting = %q, want +OK", greeting)
	}

	second, refusal := greetingOf(t, ln)
	defer second.Close()
	if !strings.HasPrefix(refusal, "-ERR") || !strings.Contains(refusal, "too many connections") {
		t.Errorf("second connection greeting = %q, want an -ERR naming the cap", refusal)
	}
}

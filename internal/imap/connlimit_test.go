package imap

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"hermex/internal/connlimit"
	"hermex/internal/directory"
)

// cappedServer serves IMAP with a connection cap of one per client address, and
// returns the listener.
func cappedServer(t *testing.T) net.Listener {
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

// firstLine dials the listener and returns the first line the server sends.
func firstLine(t *testing.T, ln net.Listener) (net.Conn, string) {
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

// TestConnCapRefusesWithBye is the load-bearing case: a client past the cap is told
// why in the greeting RFC 3501 reserves for a server that will not serve the
// connection, rather than seeing a bare disconnect.
func TestConnCapRefusesWithBye(t *testing.T) {
	ln := cappedServer(t)

	first, greeting := firstLine(t, ln)
	defer first.Close()
	if !strings.HasPrefix(greeting, "* OK") {
		t.Fatalf("first connection greeting = %q, want an OK greeting", greeting)
	}

	second, refusal := firstLine(t, ln)
	defer second.Close()
	if !strings.HasPrefix(refusal, "* BYE") {
		t.Errorf("second connection greeting = %q, want an untagged BYE", refusal)
	}
	if !strings.Contains(refusal, "too many connections") {
		t.Errorf("refusal = %q, want it to name the cap", refusal)
	}
}

// TestConnCapAdmitsAfterAClose proves the cap is a concurrency bound, not a
// lifetime one: closing the first connection frees the slot.
func TestConnCapAdmitsAfterAClose(t *testing.T) {
	ln := cappedServer(t)

	first, _ := firstLine(t, ln)
	_ = first.Close()

	// The release runs when the handler returns, which happens once the server
	// notices the close, so retry briefly rather than racing it.
	var greeting string
	for range 100 {
		conn, line := firstLine(t, ln)
		greeting = line
		_ = conn.Close()
		if strings.HasPrefix(greeting, "* OK") {
			return
		}
	}
	t.Errorf("after the first connection closed, a new one still got %q", greeting)
}

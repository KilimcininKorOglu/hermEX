package smtp

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"hermex/internal/connlimit"
)

// cappedSMTP serves SMTP with a connection cap of one per client address.
func cappedSMTP(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	limiter := connlimit.New()
	limiter.SetLimits(100, 1)
	limiter.SetEnabled(true)
	srv := &Server{Backend: &fakeBackend{sess: &fakeSession{}}, Hostname: "mail.test"}
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

// TestConnCapRefusesWith421 proves a sender past the cap gets 421, the reply RFC
// 5321 gives when the service is not available and the channel is closing. The
// code matters: a 4xx makes the sending server retry later, where a 5xx would
// bounce mail that a busy minute should only have delayed.
func TestConnCapRefusesWith421(t *testing.T) {
	ln := cappedSMTP(t)

	first, greeting := greetingOf(t, ln)
	defer first.Close()
	if !strings.HasPrefix(greeting, "220") {
		t.Fatalf("first connection greeting = %q, want 220", greeting)
	}

	second, refusal := greetingOf(t, ln)
	defer second.Close()
	if !strings.HasPrefix(refusal, "421") {
		t.Errorf("second connection greeting = %q, want 421", refusal)
	}
	if !strings.Contains(refusal, "mail.test") {
		t.Errorf("refusal = %q, want it to name the server", refusal)
	}
}

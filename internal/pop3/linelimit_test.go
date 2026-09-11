package pop3

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// cappedLineServer serves POP3 with a small command-line cap.
func cappedLineServer(t *testing.T, limit int64) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &Server{Auth: directory.StaticAccounts{}, Hostname: "mail.test"}
	srv.SetMaxCommandLine(limit)
	go func() { _ = srv.Serve(ln) }()
	return ln
}

// dialPOP3Line opens a connection and reads the greeting.
func dialPOP3Line(t *testing.T, ln net.Listener) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	return conn, br
}

// TestLongCommandLineIsRefused is the load-bearing case: a line past the cap is
// answered rather than accumulated, and the connection keeps serving.
func TestLongCommandLineIsRefused(t *testing.T) {
	ln := cappedLineServer(t, 128)
	conn, br := dialPOP3Line(t, ln)

	if _, err := conn.Write([]byte("USER " + strings.Repeat("x", 500) + "\r\nCAPA\r\n")); err != nil {
		t.Fatal(err)
	}
	refusal, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read after a long line: %v", err)
	}
	if !strings.HasPrefix(refusal, "-ERR") {
		t.Errorf("answer = %q, want -ERR", refusal)
	}
	next, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read the next command's answer: %v", err)
	}
	if !strings.HasPrefix(next, "+OK") {
		t.Errorf("next command answered %q, want +OK", next)
	}
}

// TestShortCommandIsUnaffected proves the cap does not disturb ordinary traffic.
func TestShortCommandIsUnaffected(t *testing.T) {
	ln := cappedLineServer(t, 128)
	conn, br := dialPOP3Line(t, ln)

	if _, err := conn.Write([]byte("CAPA\r\n")); err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(line, "+OK") {
		t.Errorf("answer = %q, want +OK", line)
	}
}

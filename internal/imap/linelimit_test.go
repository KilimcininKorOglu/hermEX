package imap

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// cappedLineServer serves IMAP with a small command-line cap and returns the
// listener.
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

// dialIMAPLine opens a connection and reads the greeting.
func dialIMAPLine(t *testing.T, ln net.Listener) (net.Conn, *bufio.Reader) {
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

// TestLongCommandLineIsRefused is the load-bearing case: a client that sends more
// than the cap is answered and the connection keeps serving, instead of the server
// accumulating the line until it runs out of memory.
func TestLongCommandLineIsRefused(t *testing.T) {
	ln := cappedLineServer(t, 128)
	conn, br := dialIMAPLine(t, ln)

	if _, err := conn.Write([]byte("a NOOP " + strings.Repeat("x", 500) + "\r\n")); err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read after a long line: %v", err)
	}
	if !strings.HasPrefix(line, "* BAD") {
		t.Errorf("answer = %q, want an untagged BAD", line)
	}
}

// TestConnectionSurvivesALongLine proves the refusal leaves the connection at a
// command boundary: the next command is answered normally.
func TestConnectionSurvivesALongLine(t *testing.T) {
	ln := cappedLineServer(t, 128)
	conn, br := dialIMAPLine(t, ln)

	if _, err := conn.Write([]byte(strings.Repeat("x", 500) + "\r\nb NOOP\r\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := br.ReadString('\n'); err != nil { // the BAD
		t.Fatalf("read the refusal: %v", err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read after the refusal: %v", err)
	}
	if !strings.HasPrefix(line, "b OK") {
		t.Errorf("next command answered %q, want b OK", line)
	}
}

// TestCommandLineCapLeavesAShortCommandAlone proves the cap does not disturb
// ordinary traffic.
func TestCommandLineCapLeavesAShortCommandAlone(t *testing.T) {
	ln := cappedLineServer(t, 128)
	conn, br := dialIMAPLine(t, ln)

	if _, err := conn.Write([]byte("c NOOP\r\n")); err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(line, "c OK") {
		t.Errorf("answer = %q, want c OK", line)
	}
}

// TestCommandLineCapDoesNotBoundALiteral proves a literal keeps its own cap: the
// line cap counts the command line, not the payload the client announces on it.
func TestCommandLineCapDoesNotBoundALiteral(t *testing.T) {
	ln := cappedLineServer(t, 128)
	conn, br := dialIMAPLine(t, ln)

	// A 300-byte literal on a short command line: the payload is far past the line
	// cap, and must still be accepted.
	body := strings.Repeat("y", 300)
	if _, err := conn.Write([]byte("d LOGIN {300+}\r\n" + body + " pw\r\n")); err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(line, "Command line too long") {
		t.Errorf("the literal payload was counted against the command-line cap: %q", line)
	}
}

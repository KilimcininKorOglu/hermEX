package imap

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"sync/atomic"
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

// TestServerBoundsALiteralBeforeLogin drives the same rule through a live
// connection: an anonymous client's 200-byte literal against a 128-byte line cap
// is refused as a protocol error, answered with BYE before the server closes the
// out-of-step stream, where without the rule the server would read it and answer
// the LOGIN with NO.
func TestServerBoundsALiteralBeforeLogin(t *testing.T) {
	ln := cappedLineServer(t, 128)
	conn, br := dialIMAPLine(t, ln)
	if _, err := conn.Write([]byte("d LOGIN {200+}\r\n" + strings.Repeat("y", 200) + " pw\r\n")); err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(line, "* BYE") {
		t.Errorf("answer = %q, want BYE for a literal past the pre-login cap", line)
	}
}

// TestCommandLineCapDoesNotBoundALiteral proves a literal of an authenticated
// session keeps its own cap: the line cap counts the command line, not the
// payload the client announces on it. (Before login the line cap does bound a
// literal; TestLiteralBeforeLoginIsBoundByTheLineCap covers that.)
func TestCommandLineCapDoesNotBoundALiteral(t *testing.T) {
	var line, literal atomic.Int64
	line.Store(128)
	literal.Store(50 << 20)
	// A 300-byte literal on a short command line: the payload is far past the line
	// cap, and must still be accepted.
	r := &commandReader{
		br:         bufio.NewReader(strings.NewReader("d APPEND INBOX {300+}\r\n" + strings.Repeat("y", 300) + "\r\n")),
		bw:         bufio.NewWriter(&bytes.Buffer{}),
		maxLiteral: &literal, maxLine: &line,
		authed: func() bool { return true },
	}
	toks, err := r.readCommand()
	if err != nil {
		t.Fatalf("the literal payload was counted against the command-line cap: %v", err)
	}
	if len(toks) != 4 || len(toks[3].val) != 300 {
		t.Errorf("tokens = %d, want APPEND with a 300-byte literal", len(toks))
	}
}

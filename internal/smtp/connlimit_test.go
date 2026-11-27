package smtp

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"hermex/internal/connlimit"
)

// cappedSMTP serves SMTP with a connection cap of one per client address.
func cappedSMTP(t *testing.T) net.Listener {
	t.Helper()
	limiter := connlimit.New()
	limiter.SetLimits(100, 1)
	limiter.SetEnabled(true)
	srv := &Server{Backend: &fakeBackend{sess: &fakeSession{}}, Hostname: "mail.test"}
	srv.SetConnLimiter(limiter)
	return serveOn(t, srv)
}

// greetingOf dials the listener and returns the connection, reader, and first line.
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

// TestConnCapRefusesWith421 proves a sender past the cap gets 421, the reply RFC
// 5321 gives when the service is not available and the channel is closing. The
// refused connection is closed, the admitted connection continues serving, and
// closing the admitted connection frees the slot.
func TestConnCapRefusesWith421(t *testing.T) {
	ln := cappedSMTP(t)

	first, firstBR, greeting := greetingOf(t, ln)
	defer mustClose(t, first)
	if !strings.HasPrefix(greeting, "220 ") {
		t.Fatalf("first connection greeting = %q, want 220 ", greeting)
	}

	second, secondBR, refusal := greetingOf(t, ln)
	defer mustClose(t, second)
	if !strings.HasPrefix(refusal, "421 ") {
		t.Errorf("second connection greeting = %q, want 421 ", refusal)
	}
	if !strings.Contains(refusal, "mail.test") {
		t.Errorf("refusal = %q, want it to name the server", refusal)
	}
	if err := second.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if extra, err := secondBR.ReadString('\n'); !errors.Is(err, io.EOF) {
		t.Errorf("second connection after 421: read (%q, %v), want io.EOF", extra, err)
	}

	quitAdmitted(t, first, firstBR)

	third, thirdBR, thirdGreeting := greetingOf(t, ln)
	defer mustClose(t, third)
	if !strings.HasPrefix(thirdGreeting, "220 ") {
		t.Errorf("third connection after slot release = %q, want 220 ", thirdGreeting)
	}
	quitAdmitted(t, third, thirdBR)
}

func quitAdmitted(t *testing.T, conn net.Conn, br *bufio.Reader) {
	t.Helper()
	send(t, conn, "NOOP\r\n")
	noopReply, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(noopReply, "250 ") {
		t.Fatalf("NOOP on admitted connection = (%q, %v), want 250", noopReply, err)
	}
	send(t, conn, "QUIT\r\n")
	quitReply, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(quitReply, "221 ") {
		t.Fatalf("QUIT on admitted connection = (%q, %v), want 221", quitReply, err)
	}
	if _, err := io.ReadAll(br); err != nil {
		t.Fatalf("drain admitted connection after QUIT: %v", err)
	}
	mustClose(t, conn)
}

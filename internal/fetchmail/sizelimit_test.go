package fetchmail

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestTLSDialSourceHandshakeIsBounded proves a source that accepts the TCP connect
// and then stalls before speaking TLS fails after a bounded interval instead of
// blocking forever. The poll is a single sequential worker, so an unbounded
// handshake would starve every other tenant's fetchmail.
func TestTLSDialSourceHandshakeIsBounded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// The server accepts and then holds the connection open without ever speaking
	// TLS, until the test is done. Keeping it open past the 1s assertion window is
	// what gives the test teeth: only the handshake deadline can end the dial in
	// under a second, so an unbounded handshake would blow the budget.
	done := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		<-done
		_ = c.Close()
	}()
	t.Cleanup(func() { close(done) })

	old := opTimeout
	opTimeout = 150 * time.Millisecond
	t.Cleanup(func() { opTimeout = old })
	SetAllowInternalSources(true) // the listener is on loopback
	t.Cleanup(func() { SetAllowInternalSources(false) })

	start := time.Now()
	if _, err := tlsDialSource(ln.Addr().String(), "example.test", true); err == nil {
		t.Fatal("expected the stalled handshake to fail")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("handshake was not bounded: took %v", elapsed)
	}
}

// allowLoopback lets a test dial its own stub server, which the source-address
// guard refuses by default. It restores the production default afterwards, so a
// test that means to exercise the guard still sees it.
func allowLoopback(t *testing.T) {
	t.Helper()
	SetAllowInternalSources(true)
	t.Cleanup(func() { SetAllowInternalSources(false) })
}

// TestPOP3RetrRefusesAnOversizedMessage proves the RETR reader stops at the cap
// instead of buffering whatever the source sends, and that the session survives:
// the next message still downloads, so one oversized message costs a refusal
// rather than the account's whole poll.
func TestPOP3RetrRefusesAnOversizedMessage(t *testing.T) {
	allowLoopback(t)
	big := "From: a@example.com\r\nSubject: Big\r\n\r\n" + strings.Repeat("x", 4096)
	small := "From: b@example.com\r\nSubject: Small\r\n\r\nfits"
	host, port, _ := fakePOP3(t, []string{big, small})

	SetMaxMessage(1024)
	defer SetMaxMessage(0)

	c, err := dialPOP3(host, port, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.auth("alice", "secret"); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if _, err := c.retr(1); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("retr of an oversized message = %v, want ErrMessageTooLarge", err)
	}
	body, err := c.retr(2)
	if err != nil {
		t.Fatalf("the session did not survive the refusal: %v", err)
	}
	if string(body) != small+"\r\n" {
		t.Errorf("retr(2) = %q, want the second message", body)
	}
}

// endlessLineServer accepts one connection, sends prefix, then streams bytes
// with no line end until the client hangs up: a source that never finishes a
// status line or a listing. It reports how many bytes the client took.
func endlessLineServer(t *testing.T, prefix string) (addr string, sent *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	sent = &atomic.Int64{}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, prefix)
		go func() { _, _ = io.Copy(io.Discard, conn) }()
		chunk := []byte(strings.Repeat("A", 32<<10))
		for sent.Load() < 256<<20 {
			n, err := conn.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	return ln.Addr().String(), sent
}

// TestAnEndlessServerLineIsRefused points both clients at a source whose
// greeting (and, for POP3, UIDL listing) never ends. The client must give up
// with ErrResponseTooLarge once it has read about the message cap, instead of
// buffering the line until the worker runs out of memory.
func TestAnEndlessServerLineIsRefused(t *testing.T) {
	allowLoopback(t)
	SetMaxMessage(1024)
	defer SetMaxMessage(0)
	// The server counts bytes the kernel accepted, not bytes the client read: the
	// loopback socket buffers hold several MiB on Linux (2.5 MiB measured in the dev
	// container). 16 MiB covers that and stays far below the 256 MiB an unbounded
	// reader takes.
	limit := int64(1024 + responseSlack + 16<<20)

	addr, sent := endlessLineServer(t, "")
	host, port := splitAddr(t, addr)
	if _, err := dialPOP3(host, port, false, false); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("POP3 endless greeting: err = %v, want ErrResponseTooLarge", err)
	}
	if n := sent.Load(); n > limit {
		t.Errorf("POP3 client read %d bytes of one greeting line", n)
	}

	// A UIDL listing whose one line never ends.
	addr, sent = endlessLineServer(t, "+OK ready\r\n+OK\r\n+OK\r\n+OK\r\n1 ")
	host, port = splitAddr(t, addr)
	c, err := dialPOP3(host, port, false, false)
	mustNoErr(t, err, "dial")
	mustNoErr(t, c.auth("alice", "secret"), "auth")
	if _, err := c.uidl(); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("POP3 endless UIDL: err = %v, want ErrResponseTooLarge", err)
	}
	if n := sent.Load(); n > limit {
		t.Errorf("POP3 client read %d bytes of one UIDL listing", n)
	}

	addr, sent = endlessLineServer(t, "")
	host, port = splitAddr(t, addr)
	if _, err := dialIMAP(host, port, false, false); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("IMAP endless greeting: err = %v, want ErrResponseTooLarge", err)
	}
	if n := sent.Load(); n > limit {
		t.Errorf("IMAP client read %d bytes of one greeting line", n)
	}
}

// splitAddr splits a listener address into host and port.
func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, n
}

// TestPOP3RetrAcceptsUpToTheCap is the negative control: a message under the cap
// downloads byte for byte, so the bound refuses only what it should.
func TestPOP3RetrAcceptsUpToTheCap(t *testing.T) {
	allowLoopback(t)
	msg := "From: a@example.com\r\nSubject: Fits\r\n\r\n" + strings.Repeat("y", 900)
	host, port, _ := fakePOP3(t, []string{msg})

	SetMaxMessage(4096)
	defer SetMaxMessage(0)

	c, err := dialPOP3(host, port, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.auth("alice", "secret"); err != nil {
		t.Fatalf("auth: %v", err)
	}
	body, err := c.retr(1)
	if err != nil {
		t.Fatalf("retr under the cap: %v", err)
	}
	if string(body) != msg+"\r\n" {
		t.Errorf("retr = %q, want the message verbatim", body)
	}
}

// TestIMAPFetchRefusesAnOversizedLiteral proves the advertised literal size is
// checked before the allocation. make([]byte, n) on a server-claimed size is the
// whole exposure: the memory is spent whether or not the bytes ever arrive.
func TestIMAPFetchRefusesAnOversizedLiteral(t *testing.T) {
	allowLoopback(t)
	msg := "From: a@example.com\r\nSubject: Big\r\n\r\n" + strings.Repeat("z", 4096)
	host, port, _ := fakeIMAP(t, msg)

	SetMaxMessage(1024)
	defer SetMaxMessage(0)

	c, err := dialIMAP(host, port, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.login("alice", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := c.selectFolder("INBOX"); err != nil {
		t.Fatalf("select: %v", err)
	}
	if _, err := c.fetchBody("101"); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("fetchBody of an oversized literal = %v, want ErrMessageTooLarge", err)
	}
	// The literal was drained, so the connection is still in step.
	if err := c.logout(); err != nil {
		t.Errorf("the session did not survive the refusal: %v", err)
	}
}

// TestMaxMessageAlwaysHasACeiling proves "no operator limit" still bounds the
// read: the cap exists to stop a remote server from choosing the allocation, so
// 0 and a negative value both fall back to the built-in ceiling.
func TestMaxMessageAlwaysHasACeiling(t *testing.T) {
	defer SetMaxMessage(0)
	for _, n := range []int64{0, -1} {
		SetMaxMessage(n)
		if got := maxMessage(); got != defaultMaxMessage {
			t.Errorf("SetMaxMessage(%d) left the cap at %d, want the built-in %d", n, got, defaultMaxMessage)
		}
	}
	SetMaxMessage(2048)
	if got := maxMessage(); got != 2048 {
		t.Errorf("cap = %d, want the operator's 2048", got)
	}
}

// TestSourceDialRefusesInternalAddressesByDefault proves the worker will not dial
// a source that resolves to an internal address unless a system administrator has
// allowed it. A fetchmail entry is created by a domain-scoped admin, so without
// this the poll is a scheduled dial to any address that role names: this
// deployment's own databases and admin API, or a sweep of the internal network
// read off the failure modes.
func TestSourceDialRefusesInternalAddressesByDefault(t *testing.T) {
	host, port, _ := fakePOP3(t, []string{"From: a@example.com\r\n\r\nbody"})

	SetAllowInternalSources(false) // the production default
	if _, err := dialPOP3(host, port, false, false); err == nil {
		t.Fatal("dialled a loopback source with the block on")
	} else if !strings.Contains(err.Error(), "non-public address") {
		t.Errorf("err = %v, want the address-range refusal", err)
	}

	ihost, iport, _ := fakeIMAP(t, "From: a@example.com\r\n\r\nbody")
	if _, err := dialIMAP(ihost, iport, false, false); err == nil {
		t.Error("dialled a loopback IMAP source with the block on")
	}
}

// TestSourceDialFollowsTheOperatorSwitch is the negative control: an install that
// legitimately fetches from its own private network turns the switch on and the
// same dial succeeds, so the block is a policy rather than a wall.
func TestSourceDialFollowsTheOperatorSwitch(t *testing.T) {
	allowLoopback(t)
	host, port, _ := fakePOP3(t, []string{"From: a@example.com\r\n\r\nbody"})
	c, err := dialPOP3(host, port, false, false)
	if err != nil {
		t.Fatalf("dial with internal sources allowed: %v", err)
	}
	if err := c.quit(); err != nil {
		t.Errorf("quit: %v", err)
	}
}

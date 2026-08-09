package imap

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"hermex/internal/authlimit"
	"hermex/internal/directory"
)

// TestIMAPLoginThrottle proves repeated failed LOGINs from one client IP are
// locked out once the failure threshold is crossed, so online password guessing
// is blunted rather than admitted at network speed. The limiter is tuned low
// (two failures) to keep the test fast; every connection here shares 127.0.0.1,
// so the counter accrues across the attempts exactly as it would for one attacker.
func TestIMAPLoginThrottle(t *testing.T) {
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: t.TempDir()}}
	srv := &Server{Auth: auth, Hostname: "mail.test", Limiter: authlimit.New(2, time.Minute, time.Minute)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil { // greeting
		t.Fatal(err)
	}

	login := func(tag string) string {
		fmt.Fprintf(conn, "%s LOGIN alice wrongpass\r\n", tag)
		resp, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read %s: %v", tag, err)
		}
		return resp
	}

	// Two failures accrue without throttling (invalid credentials each time).
	for _, tag := range []string{"a", "b"} {
		if r := login(tag); !strings.Contains(r, "invalid credentials") {
			t.Fatalf("%s LOGIN = %q, want invalid credentials", tag, r)
		}
	}
	// The third attempt is refused for the client IP before the password is checked.
	if r := login("c"); !strings.Contains(r, "too many failed attempts") {
		t.Fatalf("throttled LOGIN = %q, want too many failed attempts", r)
	}
}

// TestIMAPThrottleCountsTheAccountToo proves the account axis is wired at the
// IMAP login chokepoint: once an account has piled up failures it is refused even
// though the address it came from is nowhere near its own, larger threshold. That
// is what stops guessing distributed over many source addresses, which an
// address-only counter never sees.
func TestIMAPThrottleCountsTheAccountToo(t *testing.T) {
	auth := directory.StaticAccounts{
		"alice": {Password: "secret", MailboxPath: t.TempDir()},
		"bob":   {Password: "secret", MailboxPath: t.TempDir()},
	}
	srv := &Server{Auth: auth, Hostname: "mail.test", Limiter: authlimit.New(2, time.Minute, time.Minute)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)

	// Each attempt opens its own connection, the shape a guesser uses.
	login := func(user, pass string) string {
		t.Helper()
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		if _, err := br.ReadString('\n'); err != nil { // greeting
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "a LOGIN %s %s\r\n", user, pass)
		resp, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return resp
	}

	for i := range 2 {
		if r := login("alice", "wrongpass"); !strings.Contains(r, "invalid credentials") {
			t.Fatalf("attempt %d = %q, want invalid credentials", i, r)
		}
	}
	if r := login("alice", "secret"); !strings.Contains(r, "too many failed attempts") {
		t.Errorf("locked-out account = %q, want too many failed attempts", r)
	}
	// The address carries only two failures against a threshold four times the
	// account's, so an unrelated account from the same host still signs in.
	if r := login("bob", "secret"); !strings.Contains(r, "a OK") {
		t.Errorf("unrelated account = %q, want a successful login", r)
	}
}

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

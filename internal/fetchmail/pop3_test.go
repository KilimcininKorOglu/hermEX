package fetchmail

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakePOP3 scripts a minimal POP3 server over one accepted connection: it serves the
// given messages (message n is messages[n-1]) and records the numbers it is asked to
// delete, so a client test can assert both the download and the delete path.
func fakePOP3(t *testing.T, messages []string) (host string, port int, deleted *[]int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	del := &[]int{}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		io.WriteString(conn, "+OK ready\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			switch strings.ToUpper(f[0]) {
			case "USER", "PASS":
				io.WriteString(conn, "+OK\r\n")
			case "UIDL":
				var b strings.Builder
				b.WriteString("+OK\r\n")
				for i := range messages {
					fmt.Fprintf(&b, "%d uid%d\r\n", i+1, i+1)
				}
				b.WriteString(".\r\n")
				io.WriteString(conn, b.String())
			case "RETR":
				n, _ := strconv.Atoi(f[1])
				io.WriteString(conn, "+OK\r\n"+messages[n-1]+"\r\n.\r\n")
			case "DELE":
				n, _ := strconv.Atoi(f[1])
				*del = append(*del, n)
				io.WriteString(conn, "+OK\r\n")
			case "QUIT":
				io.WriteString(conn, "+OK bye\r\n")
				return
			default:
				io.WriteString(conn, "-ERR unknown\r\n")
			}
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	pn, _ := strconv.Atoi(p)
	return h, pn, del
}

// TestPOP3Client proves the client round-trips a POP3 session: it authenticates, lists
// the unique-ids, downloads a message verbatim with CRLF endings, deletes by number, and
// quits — the operations the worker composes.
func TestPOP3Client(t *testing.T) {
	msgs := []string{
		"From: a@example.com\r\nSubject: One\r\n\r\nfirst body",
		"From: b@example.com\r\nSubject: Two\r\n\r\nsecond body",
	}
	host, port, deleted := fakePOP3(t, msgs)

	c, err := dialPOP3(host, port, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.auth("alice", "secret"); err != nil {
		t.Fatalf("auth: %v", err)
	}

	uids, err := c.uidl()
	if err != nil {
		t.Fatalf("uidl: %v", err)
	}
	if len(uids) != 2 || uids[1] != "uid1" || uids[2] != "uid2" {
		t.Errorf("uidl = %v, want {1:uid1, 2:uid2}", uids)
	}

	body, err := c.retr(1)
	if err != nil {
		t.Fatalf("retr: %v", err)
	}
	if want := msgs[0] + "\r\n"; string(body) != want {
		t.Errorf("retr(1) = %q, want %q", body, want)
	}

	if err := c.dele(1); err != nil {
		t.Fatalf("dele: %v", err)
	}
	if err := c.quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	if len(*deleted) != 1 || (*deleted)[0] != 1 {
		t.Errorf("server saw deletions %v, want [1]", *deleted)
	}
}

// TestPOP3StallTimesOut proves the post-connect I/O deadline ends a session against a server
// that accepts the connection but never responds, instead of hanging the worker forever.
func TestPOP3StallTimesOut(t *testing.T) {
	old := opTimeout
	opTimeout = 150 * time.Millisecond
	t.Cleanup(func() { opTimeout = old })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-stop // accept but never send the greeting; hold the conn open until cleanup
	}()

	h, p, _ := net.SplitHostPort(ln.Addr().String())
	pn, _ := strconv.Atoi(p)

	start := time.Now()
	if _, err := dialPOP3(h, pn, false, false); err == nil {
		t.Fatal("dial against a stalled server returned no error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("dial took %v, want it bounded by the I/O deadline", elapsed)
	}
}

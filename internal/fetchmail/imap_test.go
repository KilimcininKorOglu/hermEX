package fetchmail

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
)

// fakeIMAP scripts a minimal IMAP server over one connection: it serves two messages by
// UID and records STORE/EXPUNGE so a client test can assert the flag and delete paths.
// Each tagged reply echoes the request's tag.
func fakeIMAP(t *testing.T, body string) (host string, port int, stored *[]string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	rec := &[]string{}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		io.WriteString(conn, "* OK ready\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			tag, verb := f[0], strings.ToUpper(f[1])
			switch {
			case verb == "LOGIN":
				io.WriteString(conn, tag+" OK\r\n")
			case verb == "SELECT":
				io.WriteString(conn, "* 2 EXISTS\r\n"+tag+" OK [READ-WRITE]\r\n")
			case verb == "UID" && strings.ToUpper(f[2]) == "SEARCH":
				io.WriteString(conn, "* SEARCH 101 102\r\n"+tag+" OK\r\n")
			case verb == "UID" && strings.ToUpper(f[2]) == "FETCH":
				fmt.Fprintf(conn, "* 1 FETCH (UID %s BODY[] {%d}\r\n%s)\r\n%s OK\r\n", f[3], len(body), body, tag)
			case verb == "UID" && strings.ToUpper(f[2]) == "STORE":
				*rec = append(*rec, strings.TrimSpace(line))
				io.WriteString(conn, tag+" OK\r\n")
			case verb == "EXPUNGE":
				*rec = append(*rec, "EXPUNGE")
				io.WriteString(conn, tag+" OK\r\n")
			case verb == "LOGOUT":
				io.WriteString(conn, "* BYE\r\n"+tag+" OK\r\n")
				return
			default:
				io.WriteString(conn, tag+" BAD unknown\r\n")
			}
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	pn, _ := strconv.Atoi(p)
	return h, pn, rec
}

// TestIMAPClient proves the client round-trips an IMAP session: login, select, UID search,
// a literal-precise body fetch, marking seen, and a delete (store \Deleted + expunge).
func TestIMAPClient(t *testing.T) {
	msg := "From: a@example.com\r\nSubject: Hi\r\n\r\nbody line"
	host, port, recorded := fakeIMAP(t, msg)

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

	uids, err := c.search("ALL")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(uids) != 2 || uids[0] != "101" || uids[1] != "102" {
		t.Errorf("search = %v, want [101 102]", uids)
	}

	body, err := c.fetchBody("101")
	if err != nil {
		t.Fatalf("fetchBody: %v", err)
	}
	if string(body) != msg {
		t.Errorf("fetchBody = %q, want %q", body, msg)
	}

	if err := c.markSeen("101"); err != nil {
		t.Fatalf("markSeen: %v", err)
	}
	if err := c.deleteMessage("102"); err != nil {
		t.Fatalf("deleteMessage: %v", err)
	}
	if err := c.logout(); err != nil {
		t.Fatalf("logout: %v", err)
	}

	joined := strings.Join(*recorded, " | ")
	if !strings.Contains(joined, `STORE 101 +FLAGS (\Seen)`) {
		t.Errorf("did not record the \\Seen store: %q", joined)
	}
	if !strings.Contains(joined, `STORE 102 +FLAGS (\Deleted)`) || !strings.Contains(joined, "EXPUNGE") {
		t.Errorf("did not record the delete+expunge: %q", joined)
	}
}

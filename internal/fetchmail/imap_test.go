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
	t.Cleanup(func() { _ = ln.Close() })
	rec := &[]string{}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		_, _ = io.WriteString(conn, "* OK ready\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			if done := serveIMAPCommand(conn, rec, line, f, body); done {
				return
			}
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	pn, _ := strconv.Atoi(p)
	return h, pn, rec
}

// serveIMAPCommand answers one scripted command, reporting whether the session
// ends here (LOGOUT). Each tagged reply echoes the request's tag.
func serveIMAPCommand(conn net.Conn, rec *[]string, line string, f []string, body string) (done bool) {
	tag := f[0]
	switch strings.ToUpper(f[1]) {
	case "LOGIN":
		_, _ = io.WriteString(conn, tag+" OK\r\n")
	case "SELECT":
		_, _ = io.WriteString(conn, "* 2 EXISTS\r\n"+tag+" OK [READ-WRITE]\r\n")
	case "UID":
		serveIMAPUID(conn, rec, line, f, body)
	case "EXPUNGE":
		*rec = append(*rec, "EXPUNGE")
		_, _ = io.WriteString(conn, tag+" OK\r\n")
	case "LOGOUT":
		_, _ = io.WriteString(conn, "* BYE\r\n"+tag+" OK\r\n")
		return true
	default:
		_, _ = io.WriteString(conn, tag+" BAD unknown\r\n")
	}
	return false
}

// serveIMAPUID answers the UID sub-commands the client uses.
func serveIMAPUID(conn net.Conn, rec *[]string, line string, f []string, body string) {
	tag := f[0]
	if len(f) < 3 {
		_, _ = io.WriteString(conn, tag+" BAD unknown\r\n")
		return
	}
	switch strings.ToUpper(f[2]) {
	case "SEARCH":
		_, _ = io.WriteString(conn, "* SEARCH 101 102\r\n"+tag+" OK\r\n")
	case "FETCH":
		_, _ = fmt.Fprintf(conn, "* 1 FETCH (UID %s BODY[] {%d}\r\n%s)\r\n%s OK\r\n", f[3], len(body), body, tag)
	case "STORE":
		*rec = append(*rec, strings.TrimSpace(line))
		_, _ = io.WriteString(conn, tag+" OK\r\n")
	default:
		_, _ = io.WriteString(conn, tag+" BAD unknown\r\n")
	}
}

// TestIMAPClient proves the client round-trips an IMAP session: login, select, UID search,
// a literal-precise body fetch, marking seen, and a delete (store \Deleted + expunge).
func TestIMAPClient(t *testing.T) {
	allowLoopback(t)
	msg := "From: a@example.com\r\nSubject: Hi\r\n\r\nbody line"
	host, port, recorded := fakeIMAP(t, msg)

	c, err := dialIMAP(host, port, false, false)
	mustNoErr(t, err, "dial")
	mustNoErr(t, c.login("alice", "secret"), "login")
	mustNoErr(t, c.selectFolder("INBOX"), "select")

	uids, err := c.search("ALL")
	mustNoErr(t, err, "search")
	if len(uids) != 2 {
		t.Fatalf("search = %v, want two uids", uids)
	}
	wantEq(t, uids[0], "101", "the first searched uid")
	wantEq(t, uids[1], "102", "the second searched uid")

	body, err := c.fetchBody("101")
	mustNoErr(t, err, "fetchBody")
	wantEq(t, string(body), msg, "the fetched body")

	mustNoErr(t, c.markSeen("101"), "markSeen")
	mustNoErr(t, c.deleteMessage("102"), "deleteMessage")
	mustNoErr(t, c.logout(), "logout")

	wantRecorded(t, *recorded, `STORE 101 +FLAGS (\Seen)`, "the \\Seen store")
	wantRecorded(t, *recorded, `STORE 102 +FLAGS (\Deleted)`, "the delete flag")
	wantRecorded(t, *recorded, "EXPUNGE", "the expunge")
}

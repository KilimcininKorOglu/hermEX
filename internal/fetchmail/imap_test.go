package fetchmail

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
)

// imapSource is a scripted IMAP server over one connection: it serves two
// messages by UID with the given body and records STORE and EXPUNGE, so a test
// can assert the flag and delete paths. Each tagged reply echoes the request's tag.
type imapSource struct {
	host string
	port int
	body string
	// logoutOK is the tagged LOGOUT completion after the tag, "OK" unless a test
	// scripts a failure.
	logoutOK string

	mu  sync.Mutex
	rec []string
}

// recorded returns the STORE and EXPUNGE commands the source received.
func (s *imapSource) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.rec...)
}

func (s *imapSource) record(cmd string) {
	s.mu.Lock()
	s.rec = append(s.rec, cmd)
	s.mu.Unlock()
}

// fakeIMAP starts a scripted IMAP source serving body under UIDs 101 and 102.
func fakeIMAP(t *testing.T, body string) *imapSource {
	t.Helper()
	return startIMAPSource(t, body, "OK")
}

// startIMAPSource starts a scripted IMAP source whose tagged LOGOUT completion is
// logoutOK.
func startIMAPSource(t *testing.T, body, logoutOK string) *imapSource {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &imapSource{body: body, logoutOK: logoutOK}
	tlsCfg := serverTLS(t)
	serveOne(t, ln, func(conn net.Conn) (net.Conn, error) { return s.session(conn, tlsCfg) })
	s.host, s.port = listenerHostPort(t, ln)
	return s
}

// session answers commands until LOGOUT, the client's hang-up, or a failure.
func (s *imapSource) session(conn net.Conn, tlsCfg *tls.Config) (net.Conn, error) {
	w := &scriptWriter{w: conn}
	r := bufio.NewReader(conn)
	w.send("* OK ready\r\n")
	for w.err == nil {
		line, err := r.ReadString('\n')
		if err != nil {
			if readEnded(err) {
				return conn, nil
			}
			return conn, err
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if strings.EqualFold(f[1], "STARTTLS") {
			w.send(f[0] + " OK begin TLS\r\n")
			conn = tls.Server(conn, tlsCfg)
			r, w = bufio.NewReader(conn), &scriptWriter{w: conn, err: w.err}
			continue
		}
		if s.answer(w, line, f) {
			return conn, w.err
		}
	}
	return conn, w.err
}

// answer replies to one scripted command, reporting whether the session ends
// here (LOGOUT).
func (s *imapSource) answer(w *scriptWriter, line string, f []string) (done bool) {
	tag := f[0]
	switch strings.ToUpper(f[1]) {
	case "LOGIN":
		w.send(tag + " OK\r\n")
	case "SELECT":
		w.send("* 2 EXISTS\r\n" + tag + " OK [READ-WRITE]\r\n")
	case "UID":
		s.answerUID(w, line, f)
	case "EXPUNGE":
		s.record("EXPUNGE")
		w.send(tag + " OK\r\n")
	case "LOGOUT":
		w.send("* BYE\r\n" + tag + " " + s.logoutOK + "\r\n")
		return true
	default:
		w.send(tag + " BAD unknown\r\n")
	}
	return false
}

// answerUID replies to the UID sub-commands the client uses.
func (s *imapSource) answerUID(w *scriptWriter, line string, f []string) {
	tag := f[0]
	if len(f) < 3 {
		w.send(tag + " BAD unknown\r\n")
		return
	}
	switch strings.ToUpper(f[2]) {
	case "SEARCH":
		w.send("* SEARCH 101 102\r\n" + tag + " OK\r\n")
	case "FETCH":
		if len(f) < 4 {
			w.send(tag + " BAD missing uid\r\n")
			return
		}
		w.send(fmt.Sprintf("* 1 FETCH (UID %s BODY[] {%d}\r\n%s)\r\n%s OK\r\n", f[3], len(s.body), s.body, tag))
	case "STORE":
		s.record(strings.TrimSpace(line))
		w.send(tag + " OK\r\n")
	default:
		w.send(tag + " BAD unknown\r\n")
	}
}

// TestIMAPClient proves the client round-trips an IMAP session: login, select, UID search,
// a literal-precise body fetch, marking seen, and a delete (store \Deleted + expunge).
func TestIMAPClient(t *testing.T) {
	allowLoopback(t)
	msg := "From: a@example.com\r\nSubject: Hi\r\n\r\nbody line"
	src := fakeIMAP(t, msg)

	c, err := dialIMAP(src.host, src.port, false, false)
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

	recorded := src.recorded()
	wantRecorded(t, recorded, `STORE 101 +FLAGS (\Seen)`, "the \\Seen store")
	wantRecorded(t, recorded, `STORE 102 +FLAGS (\Deleted)`, "the delete flag")
	wantRecorded(t, recorded, "EXPUNGE", "the expunge")
}

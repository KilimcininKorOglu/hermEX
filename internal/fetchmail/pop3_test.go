package fetchmail

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// pop3Source is a scripted POP3 server over one accepted connection. It serves
// messages (message n is messages[n-1]), answers QUIT with quitReply, and records
// the numbers it is asked to delete, so a test can assert the download and the
// delete path.
type pop3Source struct {
	host      string
	port      int
	messages  []string
	quitReply string

	mu   sync.Mutex
	dels []int
}

// deleted returns the message numbers the client asked the source to delete.
func (s *pop3Source) deleted() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.dels...)
}

// fakePOP3 starts a scripted POP3 source whose QUIT succeeds.
func fakePOP3(t *testing.T, messages []string) *pop3Source {
	t.Helper()
	return startPOP3Source(t, messages, "+OK bye\r\n")
}

// startPOP3Source starts a scripted POP3 source that answers QUIT with quitReply.
func startPOP3Source(t *testing.T, messages []string, quitReply string) *pop3Source {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &pop3Source{messages: messages, quitReply: quitReply}
	tlsCfg := serverTLS(t)
	serveOne(t, ln, func(conn net.Conn) (net.Conn, error) { return s.session(conn, tlsCfg) })
	s.host, s.port = listenerHostPort(t, ln)
	return s
}

// session answers commands until QUIT, the client's hang-up, or a failure.
func (s *pop3Source) session(conn net.Conn, tlsCfg *tls.Config) (net.Conn, error) {
	w := &scriptWriter{w: conn}
	r := bufio.NewReader(conn)
	w.send("+OK ready\r\n")
	for w.err == nil {
		line, err := r.ReadString('\n')
		if err != nil {
			if readEnded(err) {
				return conn, nil
			}
			return conn, err
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if strings.EqualFold(f[0], "STLS") {
			w.send("+OK begin TLS\r\n")
			conn = tls.Server(conn, tlsCfg)
			r, w = bufio.NewReader(conn), &scriptWriter{w: conn, err: w.err}
			continue
		}
		if done, err := s.answer(w, f); done || err != nil {
			return conn, errors.Join(err, w.err)
		}
	}
	return conn, w.err
}

// answer replies to one scripted command, reporting whether the session ends
// here (QUIT). A RETR or DELE naming no message of the script is a client bug and
// ends the session with an error.
func (s *pop3Source) answer(w *scriptWriter, f []string) (done bool, err error) {
	switch strings.ToUpper(f[0]) {
	case "USER", "PASS":
		w.send("+OK\r\n")
	case "UIDL":
		lines := make([]string, 0, len(s.messages)+2)
		lines = append(lines, "+OK")
		for i := range s.messages {
			lines = append(lines, fmt.Sprintf("%d uid%d", i+1, i+1))
		}
		w.send(strings.Join(append(lines, "."), "\r\n") + "\r\n")
	case "RETR":
		n, err := messageNumber(f, len(s.messages))
		if err != nil {
			return false, err
		}
		w.send("+OK\r\n" + s.messages[n-1] + "\r\n.\r\n")
	case "DELE":
		n, err := messageNumber(f, len(s.messages))
		if err != nil {
			return false, err
		}
		s.mu.Lock()
		s.dels = append(s.dels, n)
		s.mu.Unlock()
		w.send("+OK\r\n")
	case "QUIT":
		w.send(s.quitReply)
		return true, nil
	default:
		w.send("-ERR unknown\r\n")
	}
	return false, nil
}

// messageNumber reads the message-number argument of f, which must name one of
// count messages.
func messageNumber(f []string, count int) (int, error) {
	if len(f) < 2 {
		return 0, fmt.Errorf("%s without a message number", f[0])
	}
	n, err := strconv.Atoi(f[1])
	if err != nil || n < 1 || n > count {
		return 0, fmt.Errorf("%s %q names no message of %d: %w", f[0], f[1], count, err)
	}
	return n, nil
}

// listenerHostPort splits a test listener's address into the host and numeric
// port the clients dial, failing the test when the address cannot be split.
func listenerHostPort(t *testing.T, ln net.Listener) (string, int) {
	t.Helper()
	h, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split the listener address: %v", err)
	}
	pn, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("parse the listener port %q: %v", p, err)
	}
	return h, pn
}

// TestPOP3Client proves the client round-trips a POP3 session: it authenticates, lists
// the unique-ids, downloads a message verbatim with CRLF endings, deletes by number, and
// quits, the operations the worker composes.
func TestPOP3Client(t *testing.T) {
	allowLoopback(t)
	msgs := []string{
		"From: a@example.com\r\nSubject: One\r\n\r\nfirst body",
		"From: b@example.com\r\nSubject: Two\r\n\r\nsecond body",
	}
	src := fakePOP3(t, msgs)

	c, err := dialPOP3(src.host, src.port, false, false)
	mustNoErr(t, err, "dial")
	mustNoErr(t, c.auth("alice", "secret"), "auth")

	uids, err := c.uidl()
	mustNoErr(t, err, "uidl")
	wantEq(t, len(uids), 2, "listed unique ids")
	wantEq(t, uids[1], "uid1", "the first unique id")
	wantEq(t, uids[2], "uid2", "the second unique id")

	body, err := c.retr(1)
	mustNoErr(t, err, "retr")
	wantEq(t, string(body), msgs[0]+"\r\n", "the retrieved message")

	mustNoErr(t, c.dele(1), "dele")
	mustNoErr(t, c.quit(), "quit")
	deleted := src.deleted()
	if len(deleted) != 1 {
		t.Fatalf("server saw deletions %v, want one", deleted)
	}
	wantEq(t, deleted[0], 1, "the deleted message number")
}

// TestALineBreakInACredentialIsRefused stores a password carrying CRLF and a
// second command. A line-based protocol would run that command on the remote
// account (here DELE 1), so both clients must refuse the value before writing
// anything, and the scripted server must see no deletion.
func TestALineBreakInACredentialIsRefused(t *testing.T) {
	allowLoopback(t)
	src := fakePOP3(t, []string{"Subject: keep\r\n\r\nbody"})
	c, err := dialPOP3(src.host, src.port, false, false)
	mustNoErr(t, err, "dial")
	if err := c.auth("alice", "secret\r\nDELE 1"); !errors.Is(err, ErrUnsafeCredential) {
		t.Errorf("POP3 auth with CRLF in the password: err = %v, want ErrUnsafeCredential", err)
	}
	mustNoErr(t, c.quit(), "quit")
	if deleted := src.deleted(); len(deleted) != 0 {
		t.Errorf("the injected command ran on the source: deleted %v", deleted)
	}

	im := &imapConn{}
	for name, err := range map[string]error{
		"login":  im.login("alice\n", "secret"),
		"pass":   im.login("alice", "a\x00b"),
		"folder": im.selectFolder("INBOX\r\na2 DELETE INBOX"),
	} {
		if !errors.Is(err, ErrUnsafeCredential) {
			t.Errorf("IMAP %s with a line break: err = %v, want ErrUnsafeCredential", name, err)
		}
	}
}

// TestPOP3StallTimesOut proves the post-connect I/O deadline ends a session against a server
// that accepts the connection but never responds, instead of hanging the worker forever.
func TestPOP3StallTimesOut(t *testing.T) {
	allowLoopback(t)
	old := opTimeout
	opTimeout = 150 * time.Millisecond
	t.Cleanup(func() { opTimeout = old })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Accept but never send the greeting; the session ends when the client,
	// having timed out, hangs up.
	serveOne(t, ln, func(conn net.Conn) (net.Conn, error) {
		_, err := bufio.NewReader(conn).ReadString('\n')
		if readEnded(err) {
			return conn, nil
		}
		return conn, err
	})

	h, pn := listenerHostPort(t, ln)

	start := time.Now()
	if _, err := dialPOP3(h, pn, false, false); err == nil {
		t.Fatal("dial against a stalled server returned no error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("dial took %v, want it bounded by the I/O deadline", elapsed)
	}
}

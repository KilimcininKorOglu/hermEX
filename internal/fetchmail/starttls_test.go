package fetchmail

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/tlstest"
)

// serverTLS returns a server TLS config over a throwaway self-signed certificate,
// for the scripted sources to answer STARTTLS and STLS with.
func serverTLS(t *testing.T) *tls.Config {
	t.Helper()
	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	mustNoErr(t, err, "issue a self-signed certificate")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	mustNoErr(t, err, "load the certificate")
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

// refusingSource scripts a plaintext source that refuses STARTTLS and STLS with
// reply and records every line it reads, so a test can show no credential reached it.
func refusingSource(t *testing.T, greeting string, reply func(tag string) string) (host string, port int, seen func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, err, "listen")
	var mu sync.Mutex
	var lines []string
	serveOne(t, ln, func(conn net.Conn) (net.Conn, error) {
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return conn, err
		}
		r := bufio.NewReader(conn)
		w := &scriptWriter{w: conn}
		w.send(greeting)
		for w.err == nil {
			line, err := r.ReadString('\n')
			if err != nil {
				if readEnded(err) {
					return conn, nil
				}
				return conn, err
			}
			mu.Lock()
			lines = append(lines, strings.TrimSpace(line))
			mu.Unlock()
			tag, _, _ := strings.Cut(line, " ")
			w.send(reply(tag))
		}
		return conn, w.err
	})
	h, pn := listenerHostPort(t, ln)
	return h, pn, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

// TestAPlaintextSourceWithoutSTARTTLSGetsNoPassword proves an entry with SSL off
// never sends its password in clear (RFC 3501 6.2.1, RFC 2595 4): a source that
// refuses STARTTLS or STLS fails the poll with ErrNoStartTLS, and none of the
// lines it read carries the account's password.
func TestAPlaintextSourceWithoutSTARTTLSGetsNoPassword(t *testing.T) {
	allowLoopback(t)
	// A distinctive marker stands in for the source password.
	marker := strings.Repeat("m", 3) + "-source-marker"
	deliver, got := capture()

	host, port, seen := refusingSource(t, "+OK ready\r\n", func(string) string { return "-ERR not supported\r\n" })
	cfg := directory.FetchmailEntry{Mailbox: "alice@example.com", SrcServer: host, SrcPort: port,
		SrcUser: "alice", SrcPassword: marker, Protocol: "pop3", Keep: true}
	if n, err := pollPOP3(&fakeWorkerStore{}, deliver, cfg, time.Now()); !errors.Is(err, ErrNoStartTLS) || n != 0 {
		t.Errorf("POP3 poll = %d, %v, want 0 and ErrNoStartTLS", n, err)
	}
	wantNoSecret(t, "POP3", seen(), marker)

	host, port, seen = refusingSource(t, "* OK ready\r\n", func(tag string) string { return tag + " BAD unknown\r\n" })
	cfg.SrcServer, cfg.SrcPort, cfg.Protocol = host, port, "imap"
	if n, err := pollIMAP(deliver, cfg, time.Now()); !errors.Is(err, ErrNoStartTLS) || n != 0 {
		t.Errorf("IMAP poll = %d, %v, want 0 and ErrNoStartTLS", n, err)
	}
	// The refused sessions filed nothing locally.
	wantEq(t, len(*got), 0, "messages delivered from sources that refused TLS")
	wantNoSecret(t, "IMAP", seen(), marker)
}

// wantNoSecret fails when any line a source read carries the password.
func wantNoSecret(t *testing.T, proto string, lines []string, secret string) {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, secret) {
			t.Errorf("%s source read the password in clear: %q", proto, l)
		}
	}
}

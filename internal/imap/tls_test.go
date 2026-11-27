package imap

import (
	"bufio"
	"compress/flate"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/config"
	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/tlstest"
)

// TestImplicitTLSSession proves an IMAP session runs end-to-end over an
// implicit-TLS listener: a client trusting the configured certificate completes
// the handshake, logs in, and SELECTs a populated INBOX. This exercises the path
// cmd/imap takes (a *tls.Conn flowing through the unchanged handler), which the
// serve.TLSListener handshake test alone does not.
func TestImplicitTLSSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	seedOneMessage(t, path)

	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	mustNoErr(t, "issue a self-signed certificate", err)
	tlsCfg, err := (&config.Config{TLSCert: certPath, TLSKey: keyPath}).TLSConfig()
	mustNoErr(t, "build the TLS config", err)

	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	// The server holds the TLS config too, as cmd/imap's does on port 993: the
	// implicit-TLS link must count as encrypted, or the plaintext-login refusal
	// would turn away every login there.
	srv := &Server{Auth: auth, Hostname: "mail.test", TLSConfig: tlsCfg}
	rawLn := serveWrapped(t, srv, func(l net.Listener) net.Listener { return tls.NewListener(l, tlsCfg) })

	conn, err := tls.Dial("tcp", rawLn.Addr().String(), &tls.Config{RootCAs: certPool(t, certPath), ServerName: "127.0.0.1"})
	mustNoErr(t, "dial over TLS", err)
	t.Cleanup(func() { mustClose(t, conn) })
	if v := conn.ConnectionState().Version; v < tls.VersionTLS12 {
		t.Errorf("negotiated TLS version = %#x, want >= 1.2", v)
	}

	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	c.mustOK("a1", "LOGIN alice secret")
	r := c.do("a2", "SELECT INBOX")
	if r.status != "OK" {
		t.Fatalf("SELECT status = %s, want OK", r.status)
	}
	wantLine(t, "SELECT INBOX over TLS", r.untagged, "EXISTS")
	c.mustOK("a3", "LOGOUT")
}

// seedOneMessage creates a mailbox holding a single Inbox message.
func seedOneMessage(t *testing.T, path string) {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open the mailbox", err)
	defer mustClose(t, st)
	mustAppend(t, st, int64(mapi.PrivateFIDInbox), "Subject: one\r\n\r\nbody", time.Unix(1, 0), 0)
}

type tlsServer struct {
	addr     string
	certPath string
}

// startSTARTTLSServer brings up a plaintext IMAP listener whose server has a TLS
// config, returning the dial address and the cert path for a client trust pool.
func startSTARTTLSServer(t *testing.T) tlsServer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice")
	seedOneMessage(t, path)

	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := (&config.Config{TLSCert: certPath, TLSKey: keyPath}).TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	ln := serveOn(t, &Server{Auth: auth, Hostname: "mail.test", TLSConfig: tlsCfg})
	return tlsServer{addr: ln.Addr().String(), certPath: certPath}
}

// TestStartTLSUpgrade proves CAPABILITY advertises STARTTLS, the command upgrades
// the link in place, an authenticated command runs over TLS, and STARTTLS is no
// longer advertised once encrypted.
func TestStartTLSUpgrade(t *testing.T) {
	ts := startSTARTTLSServer(t)
	conn, err := net.Dial("tcp", ts.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	capReply := c.do("a0", "CAPABILITY")
	wantEq(t, "CAPABILITY status", capReply.status, "OK")
	if !containsSub(capReply.untagged, "STARTTLS") {
		t.Errorf("CAPABILITY does not advertise STARTTLS: %v", capReply.untagged)
	}
	c.mustOK("a1", "STARTTLS")

	tconn := tls.Client(conn, &tls.Config{RootCAs: certPool(t, ts.certPath), ServerName: "127.0.0.1"})
	if err := tconn.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	tc := &testClient{t: t, conn: tconn, br: bufio.NewReader(tconn)}
	// A second STARTTLS over an encrypted connection is refused with BAD.
	if r := tc.do("a1b", "STARTTLS"); r.status != "BAD" {
		t.Errorf("second STARTTLS status = %s, want BAD", r.status)
	}
	tc.mustOK("a2", "LOGIN alice secret")
	tlsCap := tc.do("a3", "CAPABILITY")
	wantEq(t, "TLS CAPABILITY status", tlsCap.status, "OK")
	if containsSub(tlsCap.untagged, "STARTTLS") {
		t.Errorf("STARTTLS still advertised after TLS: %v", tlsCap.untagged)
	}
	tc.mustOK("a4", "LOGOUT")
}

// TestPlaintextLoginWaitsForTLS proves a server that can encrypt the link takes no
// password before STARTTLS (RFC 3501 §6.2.3): CAPABILITY announces LOGINDISABLED
// and offers no AUTH= mechanism, and LOGIN and AUTHENTICATE with the right
// password are both refused with NO, so the password is never accepted in clear.
func TestPlaintextLoginWaitsForTLS(t *testing.T) {
	ts := startSTARTTLSServer(t)
	conn, err := net.Dial("tcp", ts.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	capReply := c.do("a0", "CAPABILITY")
	wantEq(t, "plaintext CAPABILITY status", capReply.status, "OK")
	if !containsSub(capReply.untagged, "LOGINDISABLED") || containsSub(capReply.untagged, "AUTH=PLAIN") {
		t.Errorf("plaintext CAPABILITY = %v, want LOGINDISABLED and no AUTH=", capReply.untagged)
	}
	if r := c.do("a1", "LOGIN alice secret"); r.status != "NO" {
		t.Errorf("plaintext LOGIN = %q, want NO", r.status)
	}
	// AGFsaWNlAHNlY3JldA== is base64("\x00alice\x00secret").
	if r := c.do("a2", "AUTHENTICATE PLAIN AGFsaWNlAHNlY3JldA=="); r.status != "NO" {
		t.Errorf("plaintext AUTHENTICATE = %q, want NO", r.status)
	}
	if r := c.do("a3", "SELECT INBOX"); r.status == "OK" {
		t.Errorf("SELECT after the refused logins = %q, want a refusal", r.status)
	}
}

// TestLineCapSurvivesStartTLSAndCompress sets the operator's command-line cap to
// 128 bytes, then switches the connection to TLS (RFC 3501 6.2.1) and to DEFLATE
// (RFC 4978). Both rebuild the command reader over the new stream; the cap must
// carry over, so a 500-byte line is still refused with an untagged BAD.
func TestLineCapSurvivesStartTLSAndCompress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	seedOneMessage(t, path)
	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := (&config.Config{TLSCert: certPath, TLSKey: keyPath}).TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Auth: directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}, Hostname: "mail.test", TLSConfig: tlsCfg}
	srv.SetMaxCommandLine(128)
	ln := serveOn(t, srv)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	c.mustOK("a1", "STARTTLS")
	tconn := tls.Client(conn, &tls.Config{RootCAs: certPool(t, certPath), ServerName: "127.0.0.1"})
	if err := tconn.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	tc := &testClient{t: t, conn: tconn, br: bufio.NewReader(tconn)}
	wantLongLineRefused(t, tconn, tc.br, "after STARTTLS")

	tc.mustOK("a2", "LOGIN alice secret")
	mustSend(t, tconn, "a3 COMPRESS DEFLATE\r\n")
	if r := tc.collect("a3"); r.status != "OK" {
		t.Fatalf("COMPRESS status = %s, want OK (%s)", r.status, r.tagged)
	}
	fw, err := flate.NewWriter(tconn, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	fr := flate.NewReader(tc.br)
	t.Cleanup(func() { mustClose(t, fr) })
	wantLongLineRefused(t, flushingWriter{fw}, bufio.NewReader(fr), "after COMPRESS")
}

// flushingWriter flushes a DEFLATE writer after every write, so each command
// reaches the server whole.
type flushingWriter struct{ fw *flate.Writer }

func (w flushingWriter) Write(p []byte) (int, error) {
	n, err := w.fw.Write(p)
	if err != nil {
		return n, err
	}
	return n, w.fw.Flush()
}

// wantLongLineRefused sends a 500-byte command and expects the untagged BAD the
// line cap answers with.
func wantLongLineRefused(t *testing.T, w io.Writer, br *bufio.Reader, when string) {
	t.Helper()
	if n, err := w.Write([]byte("z NOOP " + strings.Repeat("x", 500) + "\r\n")); err != nil || n == 0 {
		t.Fatalf("%s: write (n=%d): %v", when, n, err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("%s: read: %v", when, err)
	}
	if !strings.HasPrefix(line, "* BAD") {
		t.Errorf("%s: a 500-byte line against a 128-byte cap answered %q, want an untagged BAD", when, line)
	}
}

// TestStartTLSRejectsPipelinedInjection proves a command pipelined behind
// STARTTLS in one segment is never executed: the distinctive tagged OK for the
// injected command must never appear (the connection is torn down instead).
func TestStartTLSRejectsPipelinedInjection(t *testing.T) {
	ts := startSTARTTLSServer(t)
	conn, err := net.Dial("tcp", ts.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	br := bufio.NewReader(conn)
	greeting, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix(t, "greeting", greeting, "* OK")
	if n, err := conn.Write([]byte("a1 STARTTLS\r\na2 NOOP\r\n")); err != nil || n == 0 {
		t.Fatalf("write pipelined STARTTLS (n=%d): %v", n, err)
	}
	mustNoErr(t, "set the read deadline", conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	// The read ends at the deadline or when the server closes; only the bytes matter.
	rest, err := io.ReadAll(conn)
	if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read after STARTTLS: %v", err)
	}
	if strings.Contains(string(rest), "a1 OK") {
		t.Errorf("STARTTLS acknowledged despite pipelined injection: %q", rest)
	}
	if strings.Contains(string(rest), "a2 OK") {
		t.Errorf("injected command executed across the TLS boundary: %q", rest)
	}
}

func containsSub(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// certPool builds a client trust pool holding one PEM certificate.
func certPool(t *testing.T, certPath string) *x509.CertPool {
	t.Helper()
	pemBytes, err := os.ReadFile(certPath)
	mustNoErr(t, "read the certificate", err)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("AppendCertsFromPEM: no cert added")
	}
	return pool
}

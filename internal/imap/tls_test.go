package imap

import (
	"bufio"
	"compress/flate"
	"crypto/tls"
	"crypto/x509"
	"fmt"
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

	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, "listen", err)
	t.Cleanup(func() { _ = rawLn.Close() })
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	// The server holds the TLS config too, as cmd/imap's does on port 993: the
	// implicit-TLS link must count as encrypted, or the plaintext-login refusal
	// would turn away every login there.
	go func() {
		_ = (&Server{Auth: auth, Hostname: "mail.test", TLSConfig: tlsCfg}).Serve(tls.NewListener(rawLn, tlsCfg))
	}()

	conn, err := tls.Dial("tcp", rawLn.Addr().String(), &tls.Config{RootCAs: certPool(t, certPath), ServerName: "127.0.0.1"})
	mustNoErr(t, "dial over TLS", err)
	t.Cleanup(func() { _ = conn.Close() })
	if v := conn.ConnectionState().Version; v < tls.VersionTLS12 {
		t.Errorf("negotiated TLS version = %#x, want >= 1.2", v)
	}

	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	c.mustOK("a1", "LOGIN alice secret")
	un, status := c.do("a2", "SELECT INBOX")
	if status != "OK" {
		t.Fatalf("SELECT status = %s, want OK", status)
	}
	wantLine(t, "SELECT INBOX over TLS", un, "EXISTS")
	c.mustOK("a3", "LOGOUT")
}

// seedOneMessage creates a mailbox holding a single Inbox message.
func seedOneMessage(t *testing.T, path string) {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open the mailbox", err)
	defer st.Close()
	_, err = st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("Subject: one\r\n\r\nbody"), time.Unix(1, 0), 0)
	mustNoErr(t, "append the message", err)
}

// startSTARTTLSServer brings up a plaintext IMAP listener whose server has a TLS
// config, returning the dial address and the cert path for a client trust pool.
func startSTARTTLSServer(t *testing.T) (addr, certPath string) {
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	go func() { _ = (&Server{Auth: auth, Hostname: "mail.test", TLSConfig: tlsCfg}).Serve(ln) }()
	return ln.Addr().String(), certPath
}

// TestStartTLSUpgrade proves CAPABILITY advertises STARTTLS, the command upgrades
// the link in place, an authenticated command runs over TLS, and STARTTLS is no
// longer advertised once encrypted.
func TestStartTLSUpgrade(t *testing.T) {
	addr, certPath := startSTARTTLSServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	if un, _ := c.do("a0", "CAPABILITY"); !containsSub(un, "STARTTLS") {
		t.Errorf("CAPABILITY does not advertise STARTTLS: %v", un)
	}
	c.mustOK("a1", "STARTTLS")

	tconn := tls.Client(conn, &tls.Config{RootCAs: certPool(t, certPath), ServerName: "127.0.0.1"})
	if err := tconn.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	tc := &testClient{t: t, conn: tconn, br: bufio.NewReader(tconn)}
	tc.mustOK("a2", "LOGIN alice secret")
	if un, _ := tc.do("a3", "CAPABILITY"); containsSub(un, "STARTTLS") {
		t.Errorf("STARTTLS still advertised after TLS: %v", un)
	}
	tc.mustOK("a4", "LOGOUT")
}

// TestPlaintextLoginWaitsForTLS proves a server that can encrypt the link takes no
// password before STARTTLS (RFC 3501 §6.2.3): CAPABILITY announces LOGINDISABLED
// and offers no AUTH= mechanism, and LOGIN and AUTHENTICATE with the right
// password are both refused with NO, so the password is never accepted in clear.
func TestPlaintextLoginWaitsForTLS(t *testing.T) {
	addr, _ := startSTARTTLSServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := &testClient{t: t, conn: conn, br: bufio.NewReader(conn)}
	c.expectUntagged("OK", "greeting")
	un, _ := c.do("a0", "CAPABILITY")
	if !containsSub(un, "LOGINDISABLED") || containsSub(un, "AUTH=PLAIN") {
		t.Errorf("plaintext CAPABILITY = %v, want LOGINDISABLED and no AUTH=", un)
	}
	if _, status := c.do("a1", "LOGIN alice secret"); status != "NO" {
		t.Errorf("plaintext LOGIN = %q, want NO", status)
	}
	// AGFsaWNlAHNlY3JldA== is base64("\x00alice\x00secret").
	if _, status := c.do("a2", "AUTHENTICATE PLAIN AGFsaWNlAHNlY3JldA=="); status != "NO" {
		t.Errorf("plaintext AUTHENTICATE = %q, want NO", status)
	}
	if _, status := c.do("a3", "SELECT INBOX"); status == "OK" {
		t.Errorf("SELECT after the refused logins = %q, want a refusal", status)
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &Server{Auth: directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}, Hostname: "mail.test", TLSConfig: tlsCfg}
	srv.SetMaxCommandLine(128)
	go func() { _ = srv.Serve(ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
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
	_, _ = fmt.Fprintf(tconn, "a3 COMPRESS DEFLATE\r\n")
	if _, status := tc.collect("a3"); status != "OK" {
		t.Fatalf("COMPRESS status = %s, want OK", status)
	}
	fw, err := flate.NewWriter(tconn, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	wantLongLineRefused(t, flushingWriter{fw}, bufio.NewReader(flate.NewReader(tc.br)), "after COMPRESS")
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
	if _, err := w.Write([]byte("z NOOP " + strings.Repeat("x", 500) + "\r\n")); err != nil {
		t.Fatal(err)
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
	addr, _ := startSTARTTLSServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil { // greeting
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("a1 STARTTLS\r\na2 NOOP\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	rest, _ := io.ReadAll(conn)
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

package imap

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
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
	go func() { _ = (&Server{Auth: auth, Hostname: "mail.test"}).Serve(tls.NewListener(rawLn, tlsCfg)) }()

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

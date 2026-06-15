package imap

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
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
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("Subject: one\r\n\r\nbody"), time.Unix(1, 0), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{TLSCert: certPath, TLSKey: keyPath}
	tlsCfg, err := cfg.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}

	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rawLn.Close() })
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	go (&Server{Auth: auth, Hostname: "mail.test"}).Serve(tls.NewListener(rawLn, tlsCfg))

	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("AppendCertsFromPEM: no cert added")
	}
	conn, err := tls.Dial("tcp", rawLn.Addr().String(), &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"})
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
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
	gotExists := false
	for _, l := range un {
		if strings.Contains(l, "EXISTS") {
			gotExists = true
		}
	}
	if !gotExists {
		t.Errorf("SELECT INBOX over TLS reported no EXISTS: %v", un)
	}
	c.mustOK("a3", "LOGOUT")
}

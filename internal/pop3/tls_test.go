package pop3

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
	"hermex/internal/objectstore"
	"hermex/internal/tlstest"
)

// startSTLSServer brings up a plaintext POP3 listener whose server has a TLS
// config, returning the dial address and the cert path for a client trust pool.
func startSTLSServer(t *testing.T) (addr, certPath string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close() // provisions the mailbox (built-in folders) for USER/PASS to open

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
	t.Cleanup(func() { ln.Close() })
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	go (&Server{Auth: auth, Hostname: "mail.test", TLSConfig: tlsCfg}).Serve(ln)
	return ln.Addr().String(), certPath
}

// TestSTLSUpgrade proves CAPA advertises STLS, the command upgrades the link,
// USER/PASS then authenticate over TLS, and STLS is no longer offered once
// encrypted.
func TestSTLSUpgrade(t *testing.T) {
	addr, certPath := startSTLSServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	mustPrefix(t, br, "+OK") // greeting

	io.WriteString(conn, "CAPA\r\n")
	if capa := readUntilDot(t, br); !strings.Contains(capa, "STLS") {
		t.Errorf("CAPA does not advertise STLS:\n%s", capa)
	}
	io.WriteString(conn, "STLS\r\n")
	mustPrefix(t, br, "+OK")

	tconn := tls.Client(conn, &tls.Config{RootCAs: certPool(t, certPath), ServerName: "127.0.0.1"})
	if err := tconn.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	tbr := bufio.NewReader(tconn)
	// After STLS the session stays in AUTHORIZATION (RFC 2595); CAPA must no
	// longer offer STLS now that the link is encrypted.
	io.WriteString(tconn, "CAPA\r\n")
	if capa := readUntilDot(t, tbr); strings.Contains(capa, "STLS") {
		t.Errorf("STLS still advertised after TLS:\n%s", capa)
	}
	io.WriteString(tconn, "USER alice\r\n")
	mustPrefix(t, tbr, "+OK")
	io.WriteString(tconn, "PASS secret\r\n")
	mustPrefix(t, tbr, "+OK")
	io.WriteString(tconn, "QUIT\r\n")
	mustPrefix(t, tbr, "+OK")
}

// TestSTLSRejectsPipelinedInjection proves a command pipelined behind STLS in a
// single segment is never executed: the injected CAPA's distinctive listing must
// never appear (the connection is torn down instead).
func TestSTLSRejectsPipelinedInjection(t *testing.T) {
	addr, _ := startSTLSServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil { // greeting
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("STLS\r\nCAPA\r\n")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	rest, _ := io.ReadAll(conn)
	if strings.Contains(string(rest), "capabilities") {
		t.Errorf("injected CAPA executed across the TLS boundary: %q", rest)
	}
}

func mustPrefix(t *testing.T, br *bufio.Reader, prefix string) string {
	t.Helper()
	l, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(l, prefix) {
		t.Fatalf("got %q, want prefix %q", l, prefix)
	}
	return l
}

func readUntilDot(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	var sb strings.Builder
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if l == ".\r\n" {
			break
		}
		sb.WriteString(l)
	}
	return sb.String()
}

func certPool(t *testing.T, certPath string) *x509.CertPool {
	t.Helper()
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("AppendCertsFromPEM: no cert added")
	}
	return pool
}

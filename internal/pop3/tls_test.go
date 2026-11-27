package pop3

import (
	"bufio"
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
	mustClose(t, st) // provisions the mailbox (built-in folders) for USER/PASS to open

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
	defer mustClose(t, conn)
	br := bufio.NewReader(conn)
	mustPrefix(t, br, "+OK") // greeting

	mustSend(t, conn, "%s", "CAPA\r\n")
	if capa := readUntilDot(t, br); !strings.Contains(capa, "STLS") {
		t.Errorf("CAPA does not advertise STLS:\n%s", capa)
	}
	mustSend(t, conn, "%s", "STLS\r\n")
	mustPrefix(t, br, "+OK")

	tconn := tls.Client(conn, &tls.Config{RootCAs: certPool(t, certPath), ServerName: "127.0.0.1"})
	if err := tconn.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	tbr := bufio.NewReader(tconn)
	// After STLS the session stays in AUTHORIZATION (RFC 2595); CAPA must no
	// longer offer STLS now that the link is encrypted.
	mustSend(t, tconn, "%s", "CAPA\r\n")
	if capa := readUntilDot(t, tbr); strings.Contains(capa, "STLS") {
		t.Errorf("STLS still advertised after TLS:\n%s", capa)
	}
	// RFC 2595 §4: STLS is valid only once; a second one on the encrypted link
	// is refused and the session carries on.
	mustSend(t, tconn, "%s", "STLS\r\n")
	mustPrefix(t, tbr, "-ERR")
	mustSend(t, tconn, "%s", "USER alice\r\n")
	mustPrefix(t, tbr, "+OK")
	mustSend(t, tconn, "%s", "PASS secret\r\n")
	mustPrefix(t, tbr, "+OK")
	mustSend(t, tconn, "%s", "QUIT\r\n")
	mustPrefix(t, tbr, "+OK")
}

// TestPlaintextLoginWaitsForSTLS proves a server that can encrypt the link takes
// no password before STLS (RFC 2595 §4): CAPA offers neither USER nor SASL, and
// USER, PASS and AUTH with the right password are refused, so the session never
// leaves AUTHORIZATION.
func TestPlaintextLoginWaitsForSTLS(t *testing.T) {
	addr, _ := startSTLSServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	br := bufio.NewReader(conn)
	mustPrefix(t, br, "+OK") // greeting

	mustSend(t, conn, "%s", "CAPA\r\n")
	if capa := readUntilDot(t, br); strings.Contains(capa, "USER\r\n") || strings.Contains(capa, "SASL") {
		t.Errorf("plaintext CAPA offers a login method:\n%s", capa)
	}
	for _, line := range []string{"USER alice", "PASS secret", "AUTH PLAIN AGFsaWNlAHNlY3JldA=="} {
		mustSend(t, conn, "%s", line+"\r\n")
		mustPrefix(t, br, "-ERR")
	}
	mustSend(t, conn, "%s", "STAT\r\n")
	mustPrefix(t, br, "-ERR")
}

// TestImplicitTLSLoginIsAccepted serves the POP3S shape cmd/pop3 runs on port
// 995: a server holding a TLS config behind an implicit-TLS listener. The link is
// encrypted from the first byte, so USER/PASS must be accepted and CAPA must offer
// them; the plaintext-login refusal applies only to an unencrypted link.
func TestImplicitTLSLoginIsAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)
	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := (&config.Config{TLSCert: certPath, TLSKey: keyPath}).TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	auth := directory.StaticAccounts{"alice": {Password: "secret", MailboxPath: path}}
	ln := serveWrapped(t, &Server{Auth: auth, Hostname: "mail.test", TLSConfig: tlsCfg},
		func(l net.Listener) net.Listener { return tls.NewListener(l, tlsCfg) })

	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: certPool(t, certPath), ServerName: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	br := bufio.NewReader(conn)
	mustPrefix(t, br, "+OK")
	mustSend(t, conn, "%s", "CAPA\r\n")
	if capa := readUntilDot(t, br); !strings.Contains(capa, "USER\r\n") || strings.Contains(capa, "STLS") {
		t.Errorf("CAPA over implicit TLS:\n%s\nwant USER offered and no STLS", capa)
	}
	mustSend(t, conn, "%s", "USER alice\r\n")
	mustPrefix(t, br, "+OK")
	mustSend(t, conn, "%s", "PASS secret\r\n")
	mustPrefix(t, br, "+OK")
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
	// The server tears this connection down, so closing it again may report the
	// close; only a failure other than an already-closed conn is an error.
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close: %v", err)
		}
	}()
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil { // greeting
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("STLS\r\nCAPA\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// The server tears the connection down, so the read ends at EOF; a deadline
	// error would mean it kept the connection open.
	rest, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read after the pipelined STLS: %v (the connection was not torn down)", err)
	}
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
		if _, err := sb.WriteString(l); err != nil {
			t.Fatalf("collect the listing: %v", err)
		}
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

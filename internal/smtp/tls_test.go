package smtp

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"

	"hermex/internal/config"
	"hermex/internal/tlstest"
)

// startSTARTTLSServer brings up a plaintext SMTP listener whose server has a TLS
// config, returning the dial address and the cert path for a client trust pool.
func startSTARTTLSServer(t *testing.T, sess *fakeSession) (addr, certPath string) {
	t.Helper()
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
	go (&Server{Backend: &fakeBackend{sess: sess}, Hostname: "mail.test", TLSConfig: tlsCfg}).Serve(ln)
	return ln.Addr().String(), certPath
}

// TestStartTLSUpgrade proves EHLO advertises STARTTLS, the command upgrades the
// link, a mail transaction runs over TLS, and STARTTLS is no longer advertised
// once encrypted.
func TestStartTLSUpgrade(t *testing.T) {
	sess := &fakeSession{}
	addr, certPath := startSTARTTLSServer(t, sess)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := textproto.NewReader(bufio.NewReader(conn))
	if _, _, err := r.ReadResponse(220); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	fmt.Fprint(conn, "EHLO client.test\r\n")
	_, msg, err := r.ReadResponse(250)
	if err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	if !strings.Contains(msg, "STARTTLS") {
		t.Errorf("EHLO does not advertise STARTTLS:\n%s", msg)
	}
	fmt.Fprint(conn, "STARTTLS\r\n")
	if _, _, err := r.ReadResponse(220); err != nil {
		t.Fatalf("STARTTLS: %v", err)
	}

	tconn := tls.Client(conn, &tls.Config{RootCAs: certPool(t, certPath), ServerName: "127.0.0.1"})
	if err := tconn.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	tr := textproto.NewReader(bufio.NewReader(tconn))
	fmt.Fprint(tconn, "EHLO client.test\r\n")
	_, msg2, err := tr.ReadResponse(250)
	if err != nil {
		t.Fatalf("EHLO over TLS: %v", err)
	}
	if strings.Contains(msg2, "STARTTLS") {
		t.Errorf("STARTTLS still advertised after TLS:\n%s", msg2)
	}
	fmt.Fprint(tconn, "MAIL FROM:<alice@test>\r\n")
	if _, _, err := tr.ReadResponse(250); err != nil {
		t.Fatalf("MAIL over TLS: %v", err)
	}
	fmt.Fprint(tconn, "RCPT TO:<bob@test>\r\n")
	if _, _, err := tr.ReadResponse(250); err != nil {
		t.Fatalf("RCPT over TLS: %v", err)
	}
	fmt.Fprint(tconn, "QUIT\r\n")
	tr.ReadResponse(221)

	if sess.from != "alice@test" {
		t.Errorf("transaction over TLS recorded from=%q, want alice@test", sess.from)
	}
}

// TestStartTLSRejectsPipelinedInjection proves a command pipelined behind
// STARTTLS in a single segment is never executed: the injected MAIL must never
// draw a 250 acceptance (the connection is torn down instead).
func TestStartTLSRejectsPipelinedInjection(t *testing.T) {
	sess := &fakeSession{}
	addr, _ := startSTARTTLSServer(t, sess)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil { // greeting
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("STARTTLS\r\nMAIL FROM:<evil@test>\r\n")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	rest, _ := io.ReadAll(conn)
	if strings.Contains(string(rest), "250") {
		t.Errorf("injected MAIL accepted across the TLS boundary: %q", rest)
	}
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

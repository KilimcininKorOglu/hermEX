package smtp

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
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
func startSTARTTLSServer(t *testing.T, sess *fakeSession) testTLSServer {
	t.Helper()
	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := (&config.Config{TLSCert: certPath, TLSKey: keyPath}).TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	ln := serveOn(t, &Server{Backend: &fakeBackend{sess: sess}, Hostname: "mail.test", TLSConfig: tlsCfg})
	return testTLSServer{addr: ln.Addr().String(), certPath: certPath}
}

// TestStartTLSUpgrade proves EHLO advertises STARTTLS, STARTTLS discards prior
// greeting and transaction state (RFC 3207 §4.2), upgrades the link, refuses a
// second STARTTLS, and runs a mail transaction over TLS.
func TestStartTLSUpgrade(t *testing.T) {
	sess := &fakeSession{}
	srv := startSTARTTLSServer(t, sess)
	conn, err := net.Dial("tcp", srv.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	r := textproto.NewReader(bufio.NewReader(conn))
	readCodedReply(t, r, 220, "greeting")
	send(t, conn, "EHLO client.test\r\n")
	msg := readCodedReply(t, r, 250, "EHLO")
	if !strings.Contains(msg, "STARTTLS") {
		t.Errorf("EHLO does not advertise STARTTLS:\n%s", msg)
	}
	send(t, conn, "MAIL FROM:<pre-tls@test>\r\n")
	readCodedReply(t, r, 250, "pre-TLS MAIL")

	send(t, conn, "STARTTLS\r\n")
	readCodedReply(t, r, 220, "STARTTLS")

	tconn := tls.Client(conn, &tls.Config{RootCAs: certPool(t, srv.certPath), ServerName: "127.0.0.1"})
	if err := tconn.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	tr := textproto.NewReader(bufio.NewReader(tconn))

	// RFC 3207 §4.2: pre-TLS state is discarded, so RCPT/MAIL before re-EHLO are 503.
	send(t, tconn, "RCPT TO:<bob@test>\r\n")
	readCodedReply(t, tr, 503, "RCPT after STARTTLS reset")
	wantEq(t, sess.from, "", "pre-TLS sender cleared by STARTTLS")
	send(t, tconn, "MAIL FROM:<alice@test>\r\n")
	readCodedReply(t, tr, 503, "MAIL before post-TLS EHLO")
	wantEq(t, sess.from, "", "sender after 503")

	send(t, tconn, "STARTTLS\r\n")
	readCodedReply(t, tr, 502, "second STARTTLS")

	send(t, tconn, "EHLO client.test\r\n")
	msg2 := readCodedReply(t, tr, 250, "EHLO over TLS")
	if strings.Contains(msg2, "STARTTLS") {
		t.Errorf("STARTTLS still advertised after TLS:\n%s", msg2)
	}
	send(t, tconn, "MAIL FROM:<alice@test>\r\n")
	readCodedReply(t, tr, 250, "MAIL over TLS")
	send(t, tconn, "RCPT TO:<bob@test>\r\n")
	readCodedReply(t, tr, 250, "RCPT over TLS")
	send(t, tconn, "QUIT\r\n")
	readCodedReply(t, tr, 221, "QUIT over TLS")

	wantEq(t, sess.from, "alice@test", "the envelope sender recorded over TLS")
}

// TestStartTLSRejectsPipelinedInjection proves a command pipelined behind
// STARTTLS in a single segment is never executed: the server aborts without
// sending 220 or executing the injected MAIL.
func TestStartTLSRejectsPipelinedInjection(t *testing.T) {
	sess := &fakeSession{}
	srv := startSTARTTLSServer(t, sess)
	conn, err := net.Dial("tcp", srv.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	br := bufio.NewReader(conn)
	greeting, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(greeting, "220 ") {
		t.Fatalf("greeting = (%q, %v), want 220 ", greeting, err)
	}
	if _, err := conn.Write([]byte("EHLO client.test\r\n")); err != nil {
		t.Fatal(err)
	}
	tr := textproto.NewReader(br)
	readCodedReply(t, tr, 250, "EHLO before pipelined STARTTLS")

	if _, err := conn.Write([]byte("STARTTLS\r\nMAIL FROM:<evil@test>\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set the read deadline: %v", err)
	}
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read after the pipelined STARTTLS: %v (the connection was not torn down)", err)
	}
	if len(rest) != 0 {
		t.Errorf("server answered pipelined STARTTLS with %q, want immediate close", rest)
	}
	wantEq(t, sess.from, "", "injected sender")
}

// TestStartTLSUnavailableWithoutConfig verifies STARTTLS is neither advertised
// nor accepted when TLSConfig is nil.
func TestStartTLSUnavailableWithoutConfig(t *testing.T) {
	r, conn := dialServer(t, &fakeSession{})
	readCodedReply(t, r, 220, "greeting")
	send(t, conn, "EHLO client.test\r\n")
	ehlo := readCodedReply(t, r, 250, "EHLO")
	if strings.Contains(ehlo, "STARTTLS") {
		t.Errorf("STARTTLS advertised without TLSConfig:\n%s", ehlo)
	}
	send(t, conn, "STARTTLS\r\n")
	readCodedReply(t, r, 502, "STARTTLS without TLSConfig")
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

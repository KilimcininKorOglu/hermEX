package smtp

import (
	"bufio"
	"encoding/base64"
	"net"
	"net/textproto"
	"testing"

	"hermex/internal/config"
	"hermex/internal/tlstest"
)

// startSubmissionServer serves one submission listener (RFC 6409) and one
// ordinary listener from the same server, as the MTA does on 587 and 25.
func startSubmissionServer(t *testing.T, sess Session) (submission, inbound, certPath string) {
	t.Helper()
	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := (&config.Config{TLSCert: certPath, TLSKey: keyPath}).TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	listen := func() net.Listener {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		return ln
	}
	sub, in := listen(), listen()
	srv := &Server{Backend: sessionBackend{sess}, Hostname: "mail.test", TLSConfig: tlsCfg}
	srv.AddSubmissionListener(sub)
	srv.AddListener(in)
	go func() { _ = srv.Start() }()
	t.Cleanup(func() { _ = sub.Close(); _ = in.Close() })
	return sub.Addr().String(), in.Addr().String(), certPath
}

// TestSubmissionRequiresAuthBeforeMail follows RFC 6409 §4.3 and RFC 4954 §6: on
// the submission port a MAIL before AUTH is refused with 530, in plaintext and
// over TLS, and the same MAIL is accepted once the client has authenticated.
func TestSubmissionRequiresAuthBeforeMail(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	sub, _, certPath := startSubmissionServer(t, sess)

	conn, err := net.Dial("tcp", sub)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := textproto.NewReader(bufio.NewReader(conn))
	expect(t, r, 220)
	send(t, conn, "EHLO c\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 530)

	tr, tconn := dialTLS(t, sub, certPath)
	send(t, tconn, "EHLO c\r\n")
	expect(t, tr, 250)
	send(t, tconn, "MAIL FROM:<alice@test>\r\n")
	expect(t, tr, 530)

	cred := base64.StdEncoding.EncodeToString([]byte("\x00alice@test\x00wrong"))
	send(t, tconn, "AUTH PLAIN "+cred+"\r\n")
	expect(t, tr, 535)
	send(t, tconn, "MAIL FROM:<alice@test>\r\n")
	expect(t, tr, 530)

	cred = base64.StdEncoding.EncodeToString([]byte("\x00alice@test\x00secret"))
	send(t, tconn, "AUTH PLAIN "+cred+"\r\n")
	expect(t, tr, 235)
	send(t, tconn, "MAIL FROM:<alice@test>\r\n")
	expect(t, tr, 250)
}

// TestInboundPortTakesMailWithoutAuth keeps port 25 an MX: the submission rule
// must not reach the ordinary listener of the same server.
func TestInboundPortTakesMailWithoutAuth(t *testing.T) {
	_, in, _ := startSubmissionServer(t, &authSession{user: "alice@test", pass: "secret"})
	conn, err := net.Dial("tcp", in)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := textproto.NewReader(bufio.NewReader(conn))
	expect(t, r, 220)
	send(t, conn, "EHLO c\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<sender@example.com>\r\n")
	expect(t, r, 250)
}

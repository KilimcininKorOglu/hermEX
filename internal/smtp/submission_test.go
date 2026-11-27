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

type submissionServer struct {
	submission string
	inbound    string
	certPath   string
}

// startSubmissionServer serves one submission listener (RFC 6409) and one
// ordinary listener from the same server, as the MTA does on 587 and 25.
func startSubmissionServer(t *testing.T, sess Session) submissionServer {
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
	startServer(t, srv, sub, in)
	return submissionServer{
		submission: sub.Addr().String(),
		inbound:    in.Addr().String(),
		certPath:   certPath,
	}
}

// TestSubmissionRequiresAuthBeforeMail follows RFC 6409 §4.3 and RFC 4954 §6: on
// the submission port a MAIL before AUTH is refused with 530 5.7.0, in plaintext
// and over TLS, and the same MAIL is accepted once the client has authenticated.
func TestSubmissionRequiresAuthBeforeMail(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	srv := startSubmissionServer(t, sess)

	conn, err := net.Dial("tcp", srv.submission)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	r := textproto.NewReader(bufio.NewReader(conn))
	expect(t, r, 220)
	send(t, conn, "EHLO c\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	wantReplyPrefix(t, readCodedReply(t, r, 530, "plaintext submission MAIL"), "5.7.0 ", "plaintext 530 code")
	wantEq(t, sess.from, "", "sender after plaintext 530")
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 503)
	send(t, conn, "DATA\r\n")
	expect(t, r, 503)
	send(t, conn, "BDAT 5 LAST\r\nhello")
	expect(t, r, 503)
	wantEq(t, sess.delivered, 0, "delivered before auth")

	tr, tconn := dialTLS(t, srv.submission, srv.certPath)
	send(t, tconn, "EHLO c\r\n")
	expect(t, tr, 250)
	send(t, tconn, "MAIL FROM:<alice@test>\r\n")
	wantReplyPrefix(t, readCodedReply(t, tr, 530, "TLS submission MAIL"), "5.7.0 ", "TLS 530 code")
	wantEq(t, sess.from, "", "sender after TLS 530")

	cred := base64.StdEncoding.EncodeToString([]byte("\x00alice@test\x00wrong"))
	send(t, tconn, "AUTH PLAIN "+cred+"\r\n")
	expect(t, tr, 535)
	wantEq(t, sess.authed, "", "authed after wrong password")
	send(t, tconn, "MAIL FROM:<alice@test>\r\n")
	wantReplyPrefix(t, readCodedReply(t, tr, 530, "MAIL after failed AUTH"), "5.7.0 ", "post-fail 530 code")
	wantEq(t, sess.from, "", "sender after failed AUTH")

	cred = base64.StdEncoding.EncodeToString([]byte("\x00alice@test\x00secret"))
	send(t, tconn, "AUTH PLAIN "+cred+"\r\n")
	expect(t, tr, 235)
	send(t, tconn, "MAIL FROM:<alice@test>\r\n")
	expect(t, tr, 250)
	wantEq(t, sess.authed, "alice@test", "authenticated user")
	wantEq(t, sess.from, "alice@test", "sender after AUTH")
}

// TestInboundPortTakesMailWithoutAuth keeps port 25 an MX: the submission rule
// must not reach the ordinary listener of the same server.
func TestInboundPortTakesMailWithoutAuth(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	srv := startSubmissionServer(t, sess)
	conn, err := net.Dial("tcp", srv.inbound)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	r := textproto.NewReader(bufio.NewReader(conn))
	expect(t, r, 220)
	send(t, conn, "EHLO c\r\n")
	expect(t, r, 250)
	send(t, conn, "MAIL FROM:<sender@example.com>\r\n")
	expect(t, r, 250)
	wantEq(t, sess.from, "sender@example.com", "inbound sender")
}

package smtp

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/textproto"
	"strings"
	"testing"

	"hermex/internal/config"
	"hermex/internal/tlstest"
)

// authSession is a fakeSession that can validate one credential, recording who
// authenticated.
type authSession struct {
	fakeSession
	user, pass string
	authed     string
}

func (s *authSession) Auth(user, password string) bool {
	if user == s.user && password == s.pass {
		s.authed = user
		return true
	}
	return false
}

type sessionBackend struct{ sess Session }

func (b sessionBackend) NewSession(string) (Session, error) { return b.sess, nil }

type testTLSServer struct {
	addr     string
	certPath string
}

// startAuthServer starts a STARTTLS-capable server driving the given session.
func startAuthServer(t *testing.T, sess Session, maxLine int64) testTLSServer {
	t.Helper()
	certPath, keyPath, err := tlstest.SelfSigned(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := (&config.Config{TLSCert: certPath, TLSKey: keyPath}).TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Backend: sessionBackend{sess}, Hostname: "mail.test", TLSConfig: tlsCfg}
	if maxLine > 0 {
		srv.SetMaxCommandLine(maxLine)
	}
	ln := serveOn(t, srv)
	return testTLSServer{addr: ln.Addr().String(), certPath: certPath}
}

// dialTLS connects, completes the STARTTLS upgrade, and returns a reader over the
// secured link.
func dialTLS(t *testing.T, addr, certPath string) (*textproto.Reader, net.Conn) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	r := textproto.NewReader(bufio.NewReader(conn))
	expect(t, r, 220)
	send(t, conn, "EHLO c\r\n")
	expect(t, r, 250)
	send(t, conn, "STARTTLS\r\n")
	expect(t, r, 220)
	tconn := tls.Client(conn, &tls.Config{RootCAs: certPool(t, certPath), ServerName: "127.0.0.1"})
	if err := tconn.Handshake(); err != nil {
		mustClose(t, conn)
		t.Fatalf("client handshake: %v", err)
	}
	t.Cleanup(func() { mustClose(t, tconn) })
	return textproto.NewReader(bufio.NewReader(tconn)), tconn
}

// TestAuthPlainSuccess proves AUTH is advertised over TLS and a valid PLAIN
// credential authenticates.
func TestAuthPlainSuccess(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	srv := startAuthServer(t, sess, 0)
	tr, tconn := dialTLS(t, srv.addr, srv.certPath)

	send(t, tconn, "EHLO c\r\n")
	msg := readCodedReply(t, tr, 250, "EHLO over TLS")
	if !strings.Contains(msg, "AUTH PLAIN LOGIN") {
		t.Errorf("EHLO over TLS does not advertise AUTH PLAIN LOGIN:\n%s", msg)
	}

	cred := base64.StdEncoding.EncodeToString([]byte("\x00alice@test\x00secret"))
	send(t, tconn, "AUTH PLAIN "+cred+"\r\n")
	wantReplyPrefix(t, readCodedReply(t, tr, 235, "AUTH PLAIN valid"), "2.7.0 ", "AUTH PLAIN success code")
	wantEq(t, sess.authed, "alice@test", "authenticated user")
}

// TestAuthPlainFailure proves a wrong password is rejected with 535.
func TestAuthPlainFailure(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	srv := startAuthServer(t, sess, 0)
	tr, tconn := dialTLS(t, srv.addr, srv.certPath)

	cred := base64.StdEncoding.EncodeToString([]byte("\x00alice@test\x00wrong"))
	send(t, tconn, "AUTH PLAIN "+cred+"\r\n")
	wantReplyPrefix(t, readCodedReply(t, tr, 535, "AUTH PLAIN invalid"), "5.7.8 ", "AUTH PLAIN failure code")
	wantEq(t, sess.authed, "", "failed AUTH identity")
}

// TestAuthLoginSuccess proves the LOGIN challenge-response authenticates.
func TestAuthLoginSuccess(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	srv := startAuthServer(t, sess, 0)
	tr, tconn := dialTLS(t, srv.addr, srv.certPath)

	send(t, tconn, "AUTH LOGIN\r\n")
	wantEq(t, readCodedReply(t, tr, 334, "AUTH LOGIN username challenge"), "VXNlcm5hbWU6", "username challenge")
	send(t, tconn, base64.StdEncoding.EncodeToString([]byte("alice@test"))+"\r\n")
	wantEq(t, readCodedReply(t, tr, 334, "AUTH LOGIN password challenge"), "UGFzc3dvcmQ6", "password challenge")
	send(t, tconn, base64.StdEncoding.EncodeToString([]byte("secret"))+"\r\n")
	wantReplyPrefix(t, readCodedReply(t, tr, 235, "AUTH LOGIN valid"), "2.7.0 ", "AUTH LOGIN success code")
	wantEq(t, sess.authed, "alice@test", "authenticated user")
}

// TestAuthRejectedWithoutTLS proves AUTH is neither advertised nor accepted on a
// plaintext link.
func TestAuthRejectedWithoutTLS(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	srv := startAuthServer(t, sess, 0)
	conn, err := net.Dial("tcp", srv.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, conn)
	r := textproto.NewReader(bufio.NewReader(conn))
	expect(t, r, 220)
	send(t, conn, "EHLO c\r\n")
	msg := readCodedReply(t, r, 250, "EHLO")
	if strings.Contains(msg, "AUTH") {
		t.Errorf("AUTH advertised on a plaintext link:\n%s", msg)
	}
	send(t, conn, "AUTH PLAIN AGFsaWNlQHRlc3QAc2VjcmV0\r\n")
	wantReplyPrefix(t, readCodedReply(t, r, 538, "AUTH on a plaintext link"), "5.7.11 ", "AUTH without TLS code")
	wantEq(t, sess.authed, "", "unauthenticated session identity")
}

// TestAuthHostileInputs checks RFC 4954 / RFC 4616 rejections for malformed,
// aborted, unknown-mechanism, proxy-authzid, and overlong AUTH exchanges.
func TestAuthHostileInputs(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	cases := []struct {
		name  string
		steps []authStep
	}{
		{"unknown mechanism", []authStep{{"AUTH CRAM-MD5\r\n", 504}}},
		{"invalid base64", []authStep{{"AUTH PLAIN !!!not-base64!!!\r\n", 501}}},
		{"proxy authzid", []authStep{{"AUTH PLAIN " + b64([]byte("bob@test\x00alice@test\x00secret")) + "\r\n", 501}}},
		{"empty password", []authStep{{"AUTH PLAIN " + b64([]byte("\x00alice@test\x00")) + "\r\n", 501}}},
		{"plain abort", []authStep{{"AUTH PLAIN\r\n", 334}, {"*\r\n", 501}}},
		{"login invalid b64 user", []authStep{{"AUTH LOGIN\r\n", 334}, {"!!!\r\n", 501}}},
		{"login abort password", []authStep{{"AUTH LOGIN\r\n", 334}, {b64([]byte("alice@test")) + "\r\n", 334}, {"*\r\n", 501}}},
		{"login wrong password", []authStep{{"AUTH LOGIN\r\n", 334}, {b64([]byte("alice@test")) + "\r\n", 334}, {b64([]byte("wrong")) + "\r\n", 535}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := &authSession{user: "alice@test", pass: "secret"}
			srv := startAuthServer(t, sess, 0)
			tr, tconn := dialTLS(t, srv.addr, srv.certPath)
			for _, st := range tc.steps {
				send(t, tconn, st.send)
				readCodedReply(t, tr, st.wantCode, tc.name)
			}
			wantEq(t, sess.authed, "", "authed after refused "+tc.name)
		})
	}

	t.Run("overlong continuation line", func(t *testing.T) {
		sess := &authSession{user: "alice@test", pass: "secret"}
		srv := startAuthServer(t, sess, 64)
		tr, tconn := dialTLS(t, srv.addr, srv.certPath)
		send(t, tconn, "AUTH PLAIN\r\n")
		readCodedReply(t, tr, 334, "AUTH PLAIN challenge")
		send(t, tconn, strings.Repeat("A", 128)+"\r\n")
		send(t, tconn, "NOOP\r\n")
		readCodedReply(t, tr, 250, "NOOP after dropped overlong AUTH continuation")
		wantEq(t, sess.authed, "", "authed after overlong continuation")
	})
}

type authStep struct {
	send     string
	wantCode int
}

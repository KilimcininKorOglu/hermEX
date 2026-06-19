package smtp

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
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

// startAuthServer starts a STARTTLS-capable server driving the given session.
func startAuthServer(t *testing.T, sess Session) (addr, certPath string) {
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
	go (&Server{Backend: sessionBackend{sess}, Hostname: "mail.test", TLSConfig: tlsCfg}).Serve(ln)
	return ln.Addr().String(), certPath
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
	r.ReadResponse(220)
	fmt.Fprint(conn, "EHLO c\r\n")
	r.ReadResponse(250)
	fmt.Fprint(conn, "STARTTLS\r\n")
	r.ReadResponse(220)
	tconn := tls.Client(conn, &tls.Config{RootCAs: certPool(t, certPath), ServerName: "127.0.0.1"})
	if err := tconn.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	t.Cleanup(func() { tconn.Close() })
	return textproto.NewReader(bufio.NewReader(tconn)), tconn
}

// TestAuthPlainSuccess proves AUTH is advertised over TLS and a valid PLAIN
// credential authenticates.
func TestAuthPlainSuccess(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	addr, certPath := startAuthServer(t, sess)
	tr, tconn := dialTLS(t, addr, certPath)

	fmt.Fprint(tconn, "EHLO c\r\n")
	_, msg, err := tr.ReadResponse(250)
	if err != nil {
		t.Fatalf("EHLO over TLS: %v", err)
	}
	if !strings.Contains(msg, "AUTH") {
		t.Errorf("EHLO over TLS does not advertise AUTH:\n%s", msg)
	}

	cred := base64.StdEncoding.EncodeToString([]byte("\x00alice@test\x00secret"))
	fmt.Fprintf(tconn, "AUTH PLAIN %s\r\n", cred)
	if _, _, err := tr.ReadResponse(235); err != nil {
		t.Fatalf("AUTH PLAIN valid: want 235: %v", err)
	}
	if sess.authed != "alice@test" {
		t.Errorf("session recorded authed=%q, want alice@test", sess.authed)
	}
}

// TestAuthPlainFailure proves a wrong password is rejected with 535.
func TestAuthPlainFailure(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	addr, certPath := startAuthServer(t, sess)
	tr, tconn := dialTLS(t, addr, certPath)

	cred := base64.StdEncoding.EncodeToString([]byte("\x00alice@test\x00wrong"))
	fmt.Fprintf(tconn, "AUTH PLAIN %s\r\n", cred)
	if _, _, err := tr.ReadResponse(535); err != nil {
		t.Fatalf("AUTH PLAIN invalid: want 535: %v", err)
	}
	if sess.authed != "" {
		t.Errorf("a failed AUTH must not record an identity, got %q", sess.authed)
	}
}

// TestAuthLoginSuccess proves the LOGIN challenge-response authenticates.
func TestAuthLoginSuccess(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	addr, certPath := startAuthServer(t, sess)
	tr, tconn := dialTLS(t, addr, certPath)

	fmt.Fprint(tconn, "AUTH LOGIN\r\n")
	if _, _, err := tr.ReadResponse(334); err != nil {
		t.Fatalf("AUTH LOGIN username challenge: %v", err)
	}
	fmt.Fprintf(tconn, "%s\r\n", base64.StdEncoding.EncodeToString([]byte("alice@test")))
	if _, _, err := tr.ReadResponse(334); err != nil {
		t.Fatalf("AUTH LOGIN password challenge: %v", err)
	}
	fmt.Fprintf(tconn, "%s\r\n", base64.StdEncoding.EncodeToString([]byte("secret")))
	if _, _, err := tr.ReadResponse(235); err != nil {
		t.Fatalf("AUTH LOGIN valid: want 235: %v", err)
	}
}

// TestAuthRejectedWithoutTLS proves AUTH is neither advertised nor accepted on a
// plaintext link.
func TestAuthRejectedWithoutTLS(t *testing.T) {
	sess := &authSession{user: "alice@test", pass: "secret"}
	addr, _ := startAuthServer(t, sess)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := textproto.NewReader(bufio.NewReader(conn))
	r.ReadResponse(220)
	fmt.Fprint(conn, "EHLO c\r\n")
	_, msg, err := r.ReadResponse(250)
	if err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	if strings.Contains(msg, "AUTH") {
		t.Errorf("AUTH advertised on a plaintext link:\n%s", msg)
	}
	fmt.Fprint(conn, "AUTH PLAIN AGFsaWNlQHRlc3QAc2VjcmV0\r\n")
	if _, _, err := r.ReadResponse(538); err != nil {
		t.Fatalf("AUTH on a plaintext link: want 538: %v", err)
	}
}

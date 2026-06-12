package smtp

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"testing"
)

type fakeSession struct {
	from  string
	rcpts []string
	data  []byte
}

func (s *fakeSession) Mail(from string) error { s.from = from; return nil }
func (s *fakeSession) Rcpt(to string) error   { s.rcpts = append(s.rcpts, to); return nil }
func (s *fakeSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	s.data = b
	return err
}
func (s *fakeSession) Reset()        { s.from = ""; s.rcpts = nil }
func (s *fakeSession) Logout() error { return nil }

type fakeBackend struct{ sess *fakeSession }

func (b *fakeBackend) NewSession(string) (Session, error) { return b.sess, nil }

func dialServer(t *testing.T, sess *fakeSession) (*textproto.Reader, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Backend: &fakeBackend{sess: sess}, Hostname: "mail.test"}
	go srv.Serve(ln)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); ln.Close() })
	return textproto.NewReader(bufio.NewReader(conn)), conn
}

func expect(t *testing.T, r *textproto.Reader, code int) {
	t.Helper()
	if _, _, err := r.ReadResponse(code); err != nil {
		t.Fatalf("expected %d: %v", code, err)
	}
}

func TestServerTransaction(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)

	expect(t, r, 220)
	fmt.Fprint(conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "RCPT TO:<carol@test>\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "DATA\r\n")
	expect(t, r, 354)
	// "..dotstuffed" on the wire must arrive as ".dotstuffed" after unstuffing.
	fmt.Fprint(conn, "Subject: hi\r\n\r\nline one\r\n..dotstuffed\r\n.\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "QUIT\r\n")
	expect(t, r, 221)

	if sess.from != "alice@test" {
		t.Errorf("from = %q, want alice@test", sess.from)
	}
	if len(sess.rcpts) != 2 || sess.rcpts[0] != "bob@test" || sess.rcpts[1] != "carol@test" {
		t.Errorf("rcpts = %v", sess.rcpts)
	}
	want := "Subject: hi\r\n\r\nline one\r\n.dotstuffed\r\n"
	if string(sess.data) != want {
		t.Errorf("data = %q, want %q", sess.data, want)
	}
}

func TestServerSequencingAndSyntax(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)

	expect(t, r, 220)
	fmt.Fprint(conn, "EHLO client.test\r\n")
	expect(t, r, 250)
	// RCPT before MAIL is a bad sequence.
	fmt.Fprint(conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 503)
	// Malformed MAIL argument is a syntax error.
	fmt.Fprint(conn, "MAIL FROM:alice@test\r\n")
	expect(t, r, 501)
	fmt.Fprint(conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	// DATA before any RCPT is a bad sequence.
	fmt.Fprint(conn, "DATA\r\n")
	expect(t, r, 503)
	fmt.Fprint(conn, "RSET\r\n")
	expect(t, r, 250)
	fmt.Fprint(conn, "FOOBAR\r\n")
	expect(t, r, 500)
	fmt.Fprint(conn, "QUIT\r\n")
	expect(t, r, 221)
}

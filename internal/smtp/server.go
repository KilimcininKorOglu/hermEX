// Package smtp implements a minimal RFC 5321 receiving server. It handles the
// SMTP protocol only; what happens to an accepted message is decided by a
// Backend supplied by the caller (e.g. cmd/mta wiring it to the store), so the
// protocol layer stays independent of delivery and account resolution.
package smtp

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
)

// Backend creates a Session for each accepted connection.
type Backend interface {
	NewSession(remoteAddr string) (Session, error)
}

// Session carries one connection's state through its mail transactions. Mail
// begins a transaction, Rcpt adds a recipient, Data consumes the message body,
// Reset abandons the current transaction, and Logout is called once as the
// connection closes.
type Session interface {
	Mail(from string) error
	Rcpt(to string) error
	Data(r io.Reader) error
	Reset()
	Logout() error
}

// Server accepts SMTP connections and drives them against its Backend.
type Server struct {
	Backend   Backend
	Hostname  string      // announced in the greeting and EHLO; defaults to "localhost"
	MaxSize   int64       // advertised/enforced max message size in bytes; 0 means no limit
	TLSConfig *tls.Config // when non-nil, advertise (EHLO) and accept STARTTLS
}

// Serve accepts connections on l until it is closed.
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) hostname() string {
	if s.Hostname != "" {
		return s.Hostname
	}
	return "localhost"
}

func (s *Server) handle(conn net.Conn) {
	defer func() { conn.Close() }() // closes the upgraded conn after a STARTTLS swap
	w := bufio.NewWriter(conn)
	tp := textproto.NewReader(bufio.NewReader(conn))
	_, isTLS := conn.(*tls.Conn)

	sess, err := s.Backend.NewSession(conn.RemoteAddr().String())
	if err != nil {
		reply(w, 421, s.hostname()+" service not available")
		return
	}
	defer sess.Logout()

	reply(w, 220, s.hostname()+" ESMTP hermEX")

	var hasFrom bool
	var rcptCount int
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(cmd) {
		case "HELO":
			hasFrom, rcptCount = false, 0
			sess.Reset()
			reply(w, 250, s.hostname())
		case "EHLO":
			hasFrom, rcptCount = false, 0
			sess.Reset()
			s.greetEHLO(w, arg, isTLS)
		case "STARTTLS":
			if s.TLSConfig == nil || isTLS {
				reply(w, 502, "STARTTLS not available")
				continue
			}
			if tp.R.Buffered() > 0 {
				return // pipelined plaintext behind STARTTLS; abort the connection
			}
			reply(w, 220, "ready to start TLS")
			tc := tls.Server(conn, s.TLSConfig)
			if err := tc.Handshake(); err != nil {
				return // handshake failed; deferred close fires
			}
			conn = tc
			w = bufio.NewWriter(tc)
			tp = textproto.NewReader(bufio.NewReader(tc))
			isTLS = true
			// RFC 3207: discard all state negotiated before TLS; the client
			// re-issues EHLO over the secured link.
			sess.Reset()
			hasFrom, rcptCount = false, 0
		case "MAIL":
			addr, ok := extractPath(arg, "FROM:")
			if !ok {
				reply(w, 501, "syntax: MAIL FROM:<address>")
				continue
			}
			if err := sess.Mail(addr); err != nil {
				reply(w, 550, err.Error())
				continue
			}
			hasFrom, rcptCount = true, 0
			reply(w, 250, "OK")
		case "RCPT":
			if !hasFrom {
				reply(w, 503, "need MAIL before RCPT")
				continue
			}
			addr, ok := extractPath(arg, "TO:")
			if !ok {
				reply(w, 501, "syntax: RCPT TO:<address>")
				continue
			}
			if err := sess.Rcpt(addr); err != nil {
				reply(w, 550, err.Error())
				continue
			}
			rcptCount++
			reply(w, 250, "OK")
		case "DATA":
			if rcptCount == 0 {
				reply(w, 503, "need RCPT before DATA")
				continue
			}
			reply(w, 354, "end data with <CR><LF>.<CR><LF>")
			if err := s.consumeData(tp, sess); err != nil {
				if errors.Is(err, errTooLarge) {
					reply(w, 552, "message exceeds size limit")
				} else {
					reply(w, 554, "transaction failed: "+err.Error())
				}
			} else {
				reply(w, 250, "OK")
			}
			hasFrom, rcptCount = false, 0
		case "RSET":
			sess.Reset()
			hasFrom, rcptCount = false, 0
			reply(w, 250, "OK")
		case "NOOP":
			reply(w, 250, "OK")
		case "QUIT":
			reply(w, 221, s.hostname()+" closing connection")
			return
		default:
			reply(w, 500, "command not recognized")
		}
	}
}

var errTooLarge = errors.New("message too large")

// consumeData reads the dot-terminated message body and hands it to the
// session, enforcing MaxSize when set. The body is always drained so the
// protocol stays in sync even when delivery is rejected.
func (s *Server) consumeData(tp *textproto.Reader, sess Session) error {
	dot := newDotReader(tp.R)
	var r io.Reader = dot
	if s.MaxSize > 0 {
		r = &limitedReader{r: dot, remaining: s.MaxSize}
	}
	err := sess.Data(r)
	// Always drain the underlying dot-encoded body so the next command reads
	// cleanly, even when delivery was rejected or the size limit tripped.
	io.Copy(io.Discard, dot)
	return err
}

// dotReader decodes an SMTP dot-encoded message body: it removes dot-stuffing
// and stops at the "." terminator line. Unlike textproto.DotReader it preserves
// CRLF line endings, so the stored message stays byte-faithful to the wire.
type dotReader struct {
	r    *bufio.Reader
	buf  []byte
	done bool
}

func newDotReader(r *bufio.Reader) *dotReader { return &dotReader{r: r} }

func (d *dotReader) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.done {
			return 0, io.EOF
		}
		if err := d.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}

func (d *dotReader) fill() error {
	line, err := d.r.ReadString('\n')
	if len(line) == 0 {
		d.done = true
		return io.EOF
	}
	trimmed := strings.TrimRight(line, "\r\n")
	if trimmed == "." {
		// Terminator line: end of body, with no contribution to it.
		d.done = true
		return nil
	}
	line = strings.TrimPrefix(line, ".") // un-stuff a leading dot
	d.buf = append(d.buf, line...)
	if err != nil {
		// Stream ended without a terminator; emit what we have, then finish.
		d.done = true
	}
	return nil
}

func (s *Server) greetEHLO(w *bufio.Writer, arg string, isTLS bool) {
	lines := []string{
		fmt.Sprintf("%s Hello %s", s.hostname(), strings.TrimSpace(arg)),
		"PIPELINING",
		"8BITMIME",
	}
	if s.MaxSize > 0 {
		lines = append(lines, fmt.Sprintf("SIZE %d", s.MaxSize))
	}
	if s.TLSConfig != nil && !isTLS {
		lines = append(lines, "STARTTLS")
	}
	for i, l := range lines {
		sep := "-"
		if i == len(lines)-1 {
			sep = " "
		}
		fmt.Fprintf(w, "250%s%s\r\n", sep, l)
	}
	w.Flush()
}

// reply writes a single-line SMTP response and flushes it.
func reply(w *bufio.Writer, code int, msg string) {
	fmt.Fprintf(w, "%d %s\r\n", code, msg)
	w.Flush()
}

// extractPath pulls the <addr> out of a "FROM:<addr>" / "TO:<addr>" argument,
// tolerating optional whitespace and trailing ESMTP parameters.
func extractPath(arg, prefix string) (string, bool) {
	arg = strings.TrimSpace(arg)
	if len(arg) < len(prefix) || !strings.EqualFold(arg[:len(prefix)], prefix) {
		return "", false
	}
	rest := strings.TrimSpace(arg[len(prefix):])
	openIdx := strings.IndexByte(rest, '<')
	closeIdx := strings.IndexByte(rest, '>')
	if openIdx != 0 || closeIdx < 0 {
		return "", false
	}
	return rest[1:closeIdx], true
}

// errTooLarge surfaces through this reader when the message exceeds MaxSize.
type limitedReader struct {
	r         io.Reader
	remaining int64
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	if lr.remaining <= 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > lr.remaining {
		p = p[:lr.remaining]
	}
	n, err := lr.r.Read(p)
	lr.remaining -= int64(n)
	return n, err
}

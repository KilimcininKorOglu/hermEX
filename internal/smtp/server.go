// Package smtp implements a minimal RFC 5321 receiving server. It handles the
// SMTP protocol only; what happens to an accepted message is decided by a
// Backend supplied by the caller (e.g. cmd/mta wiring it to the store), so the
// protocol layer stays independent of delivery and account resolution.
package smtp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"hermex/internal/lifecycle"
	"hermex/internal/logging"
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
	Hostname  string          // announced in the greeting and EHLO; defaults to "localhost"
	TLSConfig *tls.Config     // when non-nil, advertise (EHLO) and accept STARTTLS
	Logger    *logging.Logger // central activity log; nil disables logging

	// maxSize is the advertised/enforced max message size in bytes (0 = no limit),
	// held atomically so the MTA's poll can apply an operator's edit while sessions
	// run, with no restart. Set it via SetMaxSize.
	maxSize atomic.Int64

	conns lifecycle.ConnGroup
}

// SetMaxSize sets the advertised/enforced maximum message size in bytes (0 disables
// the limit). It is safe to call concurrently with active sessions, so an operator's
// edit applies without a restart.
func (s *Server) SetMaxSize(n int64) {
	if n < 0 {
		n = 0
	}
	s.maxSize.Store(n)
}

// AddListener registers a listener (the plaintext and any implicit-TLS one) for
// Start to serve. Call it before Start.
func (s *Server) AddListener(l net.Listener) { s.conns.AddListener(l) }

// Start serves every registered listener until Shutdown, satisfying
// lifecycle.Component.
func (s *Server) Start() error { return s.conns.Start(s.handle) }

// Serve accepts connections on l until it is closed; tests drive it directly.
func (s *Server) Serve(l net.Listener) error { return s.conns.Serve(l, s.handle) }

// Shutdown stops accepting and drains in-flight sessions within ctx's deadline.
func (s *Server) Shutdown(ctx context.Context) error { return s.conns.Shutdown(ctx) }

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

	remote := conn.RemoteAddr().String()
	// event logs through the server's logger, tagged with the client address. SMTP
	// intake has no authenticated user, so the envelope sender goes in Fields, not
	// the User column. A nil logger is a no-op.
	event := func(level logging.Level, name string, f logging.Fields) {
		s.Logger.Emit(logging.Event{Level: level, Subsystem: logging.SMTP, Name: name, RemoteAddr: remote, Fields: f})
	}

	sess, err := s.Backend.NewSession(remote)
	if err != nil {
		event(logging.LevelWarn, "conn.reject", logging.Fields{"reason": err.Error()})
		reply(w, 421, s.hostname()+" service not available")
		return
	}
	defer sess.Logout()

	reply(w, 220, s.hostname()+" ESMTP hermEX")
	event(logging.LevelInfo, "conn.accept", logging.Fields{"tls": isTLS})

	// A session that can validate credentials enables AUTH — but only over TLS,
	// so the EHLO advertisement is also gated on the link being secured.
	_, canAuth := sess.(Authenticator)
	var hasFrom bool
	var rcptCount int
	// greeted records that the client has sent HELO/EHLO. RFC 5321 §4.1.4: a
	// session carrying mail transactions MUST first be initialized by EHLO, so
	// MAIL before a greeting is a 503. Non-mail commands (VRFY/EXPN/HELP) are
	// accepted without it. STARTTLS clears it (the client re-issues EHLO).
	var greeted bool
	// Trace context for the Received: header stamped at DATA time: the HELO/EHLO
	// argument names the connecting client. It is cleared by STARTTLS, which
	// discards all prior session state (RFC 3207).
	var helo string
	// BDAT/CHUNKING transaction state (RFC 3030). binaryMIME records a
	// BODY=BINARYMIME MAIL parameter, which mandates BDAT (not DATA) for the body.
	// bdatBuf accumulates the BDAT chunks of the current transaction (nil until the
	// first chunk); bdatErr marks a chunk that failed (size exceeded), so further
	// chunks are drained and dropped until RSET clears the transaction.
	var binaryMIME bool
	var bdatBuf *bytes.Buffer
	var bdatErr bool
	// resetTxn clears all envelope and body state at a transaction boundary (a new
	// MAIL, RSET, a completed DATA/BDAT, or a re-greeting), leaving the greeting
	// (greeted/helo) untouched.
	resetTxn := func() {
		hasFrom, rcptCount, binaryMIME = false, 0, false
		bdatBuf, bdatErr = nil, false
	}
	for {
		line, err := readCommandLine(tp.R)
		if errors.Is(err, errLineTooLong) {
			reply(w, 500, "5.5.2 line too long")
			continue
		}
		if err != nil {
			return
		}
		cmd, arg, _ := strings.Cut(line, " ")
		event(logging.LevelDebug, "command", logging.Fields{"cmd": strings.ToUpper(cmd)})
		switch strings.ToUpper(cmd) {
		case "HELO":
			resetTxn()
			greeted = true
			helo = arg
			sess.Reset()
			reply(w, 250, s.hostname())
		case "EHLO":
			resetTxn()
			greeted = true
			helo = arg
			sess.Reset()
			s.greetEHLO(w, arg, isTLS, canAuth && isTLS)
		case "AUTH":
			s.handleAuth(w, tp, arg, sess, isTLS, canAuth)
		case "STARTTLS":
			if s.TLSConfig == nil || isTLS {
				reply(w, 502, "STARTTLS not available")
				continue
			}
			if tp.R.Buffered() > 0 {
				event(logging.LevelWarn, "starttls.injection", nil)
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
			resetTxn()
			greeted = false
			helo = ""
			event(logging.LevelInfo, "starttls", nil)
		case "MAIL":
			if !greeted {
				reply(w, 503, "5.5.1 send HELO/EHLO first")
				continue
			}
			addr, ok := extractPath(arg, "FROM:")
			if !ok {
				reply(w, 501, "syntax: MAIL FROM:<address>")
				continue
			}
			params := esmtpParams(arg)
			// RFC 1870: when the client declares SIZE and it exceeds the
			// advertised maximum, refuse the whole transaction now with 552
			// rather than accepting MAIL/RCPT and streaming the body only to
			// reject it after the bytes have crossed the wire.
			if max := s.maxSize.Load(); max > 0 {
				if sz, ok := declaredSize(params); ok && sz > max {
					reply(w, 552, "5.3.4 message size exceeds limit")
					continue
				}
			}
			// RFC 6710 §4.1: a present-but-invalid MT-PRIORITY (malformed, out of
			// the -9..9 range, or duplicated) MUST be refused. The value itself is
			// otherwise unused: this MTA applies the default priority policy (all
			// messages at priority 0), which also satisfies the rule that an
			// untrusted sender MUST NOT upgrade a message's priority.
			if present, ok := mtPriorityValid(arg); present && !ok {
				reply(w, 501, "5.5.2 syntax error in MT-PRIORITY parameter")
				continue
			}
			if err := sess.Mail(addr); err != nil {
				replySessionErr(w, err)
				continue
			}
			resetTxn()
			hasFrom = true
			// RFC 3030: BODY=BINARYMIME commits the sender to delivering the body
			// over BDAT; a later DATA in this transaction is then a sequence error.
			binaryMIME = strings.EqualFold(params["BODY"], "BINARYMIME")
			event(logging.LevelInfo, "mail.from", logging.Fields{"from": addr})
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
				replySessionErr(w, err)
				continue
			}
			rcptCount++
			event(logging.LevelInfo, "rcpt.to", logging.Fields{"to": addr})
			reply(w, 250, "OK")
		case "DATA":
			// RFC 3030: DATA and BDAT cannot mix in one transaction, and a
			// BINARYMIME body must arrive via BDAT; both are 503 sequence errors.
			if bdatBuf != nil {
				reply(w, 503, "5.5.1 DATA not allowed after BDAT; send RSET")
				continue
			}
			if binaryMIME {
				reply(w, 503, "5.5.1 BINARYMIME requires BDAT, not DATA")
				continue
			}
			if rcptCount == 0 {
				reply(w, 503, "need RCPT before DATA")
				continue
			}
			reply(w, 354, "end data with <CR><LF>.<CR><LF>")
			rdns := lookupRDNS(remote)
			trace := buildReceived(helo, remote, rdns, s.hostname(), isTLS, time.Now())
			if err := s.consumeData(tp, sess, trace); err != nil {
				event(logging.LevelWarn, "message.reject", logging.Fields{"recipients": rcptCount, "reason": err.Error()})
				replyDataErr(w, err)
			} else {
				event(logging.LevelInfo, "message.accept", logging.Fields{"recipients": rcptCount})
				reply(w, 250, "OK")
			}
			resetTxn()
		case "BDAT":
			// RFC 3030 CHUNKING: "BDAT <chunk-size> [LAST]". The chunk's octets
			// follow the command line's CRLF directly, with no dot-stuffing and no
			// "." terminator; the receiver reads exactly chunk-size octets.
			size, last, ok := parseBDAT(arg)
			if !ok {
				// Without a valid octet count the chunk cannot be framed, so the
				// stream position is unknown; refuse rather than guess.
				reply(w, 501, "5.5.4 syntax: BDAT <chunk-size> [LAST]")
				continue
			}
			// The chunk is always read off the wire before any reply (RFC 3030: a
			// failure "MUST accept and discard the associated message data before
			// sending the appropriate 5XX or 4XX code"), or the next command read
			// would parse message bytes. It is buffered only when the transaction
			// can accept it, and accumulation is capped at the size limit so an
			// oversized declared chunk cannot exhaust memory (OWASP A05).
			max := s.maxSize.Load()
			viable := greeted && rcptCount > 0 && !bdatErr
			if viable {
				if bdatBuf == nil {
					bdatBuf = new(bytes.Buffer)
				}
				toBuf := size
				if max > 0 {
					if room := max + 1 - int64(bdatBuf.Len()); room < toBuf {
						if room < 0 {
							room = 0
						}
						toBuf = room
					}
				}
				if _, err := io.CopyN(bdatBuf, tp.R, toBuf); err != nil {
					return // truncated chunk; the stream is no longer framed
				}
				if _, err := io.CopyN(io.Discard, tp.R, size-toBuf); err != nil {
					return
				}
			} else if _, err := io.CopyN(io.Discard, tp.R, size); err != nil {
				return
			}
			switch {
			case !greeted:
				reply(w, 503, "5.5.1 send HELO/EHLO first")
			case rcptCount == 0:
				reply(w, 503, "5.5.1 need MAIL and RCPT before BDAT")
			case bdatErr:
				reply(w, 503, "5.5.0 BDAT transaction failed; send RSET")
			case max > 0 && int64(bdatBuf.Len()) > max:
				bdatErr = true // poison the transaction; later chunks drain until RSET
				reply(w, 552, "5.3.4 message size exceeds limit")
			case !last:
				reply(w, 250, fmt.Sprintf("2.0.0 %d octets received", size))
			default:
				rdns := lookupRDNS(remote)
				trace := buildReceived(helo, remote, rdns, s.hostname(), isTLS, time.Now())
				body := io.MultiReader(strings.NewReader(trace), bytes.NewReader(bdatBuf.Bytes()))
				if err := sess.Data(body); err != nil {
					event(logging.LevelWarn, "message.reject", logging.Fields{"recipients": rcptCount, "reason": err.Error()})
					replyDataErr(w, err)
				} else {
					event(logging.LevelInfo, "message.accept", logging.Fields{"recipients": rcptCount})
					reply(w, 250, "OK")
				}
				resetTxn()
			}
		case "RSET":
			sess.Reset()
			resetTxn()
			reply(w, 250, "OK")
		case "NOOP":
			reply(w, 250, "OK")
		case "QUIT":
			reply(w, 221, s.hostname()+" closing connection")
			return
		case "VRFY":
			// RFC 5321 §3.5.1/§7.3: never confirm or deny a specific address
			// (that is user enumeration). Return the privacy-preserving 252,
			// which promises only to accept and attempt delivery; a 250 or 550
			// would leak whether the mailbox exists.
			reply(w, 252, "2.1.5 Cannot VRFY user, but will accept message and attempt delivery")
		case "EXPN":
			// RFC 5321 §3.5.2/§7.3: mailing-list expansion is disabled (an
			// address-harvesting vector); 502 marks it recognized but not
			// implemented (§4.2.4), not the 500 of an unknown command.
			reply(w, 502, "5.5.1 EXPN not available")
		case "HELP":
			// RFC 5321 §4.1.1.8: a 214 help reply, recognized rather than 500.
			reply(w, 214, "2.0.0 hermEX ESMTP; supported: HELO EHLO MAIL RCPT DATA BDAT RSET NOOP QUIT (RFC 5321)")
		default:
			reply(w, 500, "command not recognized")
		}
	}
}

var errTooLarge = errors.New("message too large")

// TempError is a Session error the server reports as a temporary failure (a 4xx),
// so the sending MTA retries later, rather than a permanent rejection (a 5xx).
// Greylisting returns it from Rcpt to defer a first-contact triplet.
type TempError struct{ Message string }

func (e *TempError) Error() string { return e.Message }

// replySessionErr maps a Session error to its SMTP reply: a TempError becomes a 451
// temporary failure (the sender retries), anything else a 550 permanent rejection.
func replySessionErr(w *bufio.Writer, err error) {
	if te, ok := errors.AsType[*TempError](err); ok {
		reply(w, 451, te.Message)
		return
	}
	reply(w, 550, err.Error())
}

// replyDataErr maps a Session.Data error to the SMTP reply shared by the DATA and
// BDAT body paths: a TempError defers (451 so the sender retries), an over-size
// body is 552, and any other failure is a 554 permanent transaction failure.
func replyDataErr(w *bufio.Writer, err error) {
	if te, ok := errors.AsType[*TempError](err); ok {
		reply(w, 451, te.Message)
	} else if errors.Is(err, errTooLarge) {
		reply(w, 552, "message exceeds size limit")
	} else {
		reply(w, 554, "transaction failed: "+err.Error())
	}
}

// parseBDAT parses a "BDAT <chunk-size> [LAST]" argument into the decimal octet
// count of the chunk that follows and whether this is the final chunk. ok is false
// when the count is missing, malformed, or a trailing token other than LAST is
// present, so the caller refuses rather than misframe the chunk.
func parseBDAT(arg string) (size int64, last, ok bool) {
	fields := strings.Fields(arg)
	if len(fields) == 0 || len(fields) > 2 {
		return 0, false, false
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || n < 0 {
		return 0, false, false
	}
	if len(fields) == 2 {
		if !strings.EqualFold(fields[1], "LAST") {
			return 0, false, false
		}
		last = true
	}
	return n, last, true
}

// consumeData reads the dot-terminated message body and hands it to the
// session, enforcing MaxSize when set. The body is always drained so the
// protocol stays in sync even when delivery is rejected.
func (s *Server) consumeData(tp *textproto.Reader, sess Session, trace string) error {
	dot := newDotReader(tp.R)
	var body io.Reader = dot
	if max := s.maxSize.Load(); max > 0 {
		body = &limitedReader{r: dot, remaining: max}
	}
	// Prepend the Received: trace header OUTSIDE the size limiter, so it is neither
	// counted against the client's size budget nor truncated when the body is at
	// the limit. The dot-decoded body keeps its CRLF endings, matching the header.
	r := io.MultiReader(strings.NewReader(trace), body)
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

func (s *Server) greetEHLO(w *bufio.Writer, arg string, isTLS, authAvailable bool) {
	lines := []string{
		fmt.Sprintf("%s Hello %s", s.hostname(), strings.TrimSpace(arg)),
		"PIPELINING",
		"8BITMIME",
		"ENHANCEDSTATUSCODES",
		"SMTPUTF8",
		"CHUNKING",
		"BINARYMIME",
		"MT-PRIORITY",
	}
	if max := s.maxSize.Load(); max > 0 {
		lines = append(lines, fmt.Sprintf("SIZE %d", max))
	}
	if s.TLSConfig != nil && !isTLS {
		lines = append(lines, "STARTTLS")
	}
	if authAvailable {
		lines = append(lines, "AUTH PLAIN LOGIN")
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

// maxCommandLine is the RFC 5321 §4.5.3.1.4 limit on a command line including the
// trailing CRLF. Commands are tiny, so anything approaching this is malformed or a
// memory-exhaustion probe; the reader caps the read rather than buffering without
// bound. The DATA body's per-line limit (§4.5.3.1.6) is deliberately not enforced
// as a hard reject: major senders routinely exceed 1000 octets and total-size
// abuse is already bounded by SIZE, so a strict line cap would only break interop.
const maxCommandLine = 512

// errLineTooLong is returned by readCommandLine when a command line exceeds
// maxCommandLine; the caller answers 500 and stays in protocol sync.
var errLineTooLong = errors.New("smtp: command line too long")

// readCommandLine reads one CRLF-terminated command line from r, enforcing
// maxCommandLine. It returns the line without the trailing CRLF. When the limit
// is exceeded it drains the rest of the line and returns errLineTooLong, so the
// connection stays framed for the next command.
func readCommandLine(r *bufio.Reader) (string, error) {
	buf := make([]byte, 0, 128)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\n' {
			if n := len(buf); n > 0 && buf[n-1] == '\r' {
				buf = buf[:n-1]
			}
			return string(buf), nil
		}
		if len(buf) >= maxCommandLine {
			// Over the limit: discard the remainder of this line so the next
			// read starts at a command boundary, then report it.
			for b != '\n' {
				if b, err = r.ReadByte(); err != nil {
					return "", err
				}
			}
			return "", errLineTooLong
		}
		buf = append(buf, b)
	}
}

// reply writes a single-line SMTP response and flushes it. The server advertises
// ENHANCEDSTATUSCODES (RFC 2034), so every 2xx/4xx/5xx reply must lead with an
// RFC 3463 status code; a bare message gets the class default (2.0.0/4.0.0/5.0.0)
// while a message that already carries a specific code (e.g. "5.7.1") is left as
// is. The connection/STARTTLS 220 banner and the 354 intermediate stay bare:
// 3xx has no enhanced class and a code in the banner would shadow the domain.
func reply(w *bufio.Writer, code int, msg string) {
	if enh := defaultEnhanced(code); enh != "" && !startsWithEnhanced(msg) {
		msg = enh + " " + msg
	}
	fmt.Fprintf(w, "%d %s\r\n", code, msg)
	w.Flush()
}

// defaultEnhanced returns the class-default RFC 3463 status code for an SMTP
// reply code, or "" when none applies (3xx has no class, and the 220 banner is
// kept bare so its first token stays the domain).
func defaultEnhanced(code int) string {
	if code == 220 {
		return ""
	}
	switch code / 100 {
	case 2:
		return "2.0.0"
	case 4:
		return "4.0.0"
	case 5:
		return "5.0.0"
	}
	return ""
}

// startsWithEnhanced reports whether msg already begins with an RFC 3463 status
// code token (class.subject.detail with class 2, 4, or 5), so reply does not
// prepend a second one.
func startsWithEnhanced(msg string) bool {
	tok, _, _ := strings.Cut(msg, " ")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return parts[0] == "2" || parts[0] == "4" || parts[0] == "5"
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

// esmtpParams parses the space-separated ESMTP parameters that follow the
// <reverse-path>/<forward-path> in a MAIL FROM / RCPT TO argument, e.g.
// "SIZE=1234 BODY=8BITMIME SMTPUTF8". Keys are upper-cased; a bare keyword maps
// to an empty value. It returns nil when there are no trailing parameters.
func esmtpParams(arg string) map[string]string {
	_, after, found := strings.Cut(arg, ">")
	if !found {
		return nil
	}
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return nil
	}
	params := make(map[string]string, len(fields))
	for _, f := range fields {
		k, v, _ := strings.Cut(f, "=")
		params[strings.ToUpper(k)] = v
	}
	return params
}

// declaredSize returns the SIZE= value (RFC 1870) from a MAIL FROM parameter set,
// and whether it was present and well-formed.
func declaredSize(params map[string]string) (int64, bool) {
	v, ok := params["SIZE"]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// mtPriorityValid inspects the MT-PRIORITY parameter (RFC 6710) in a MAIL FROM
// argument. present reports whether any MT-PRIORITY parameter appears; ok reports
// whether it is well-formed: a single occurrence whose value is a decimal integer
// in [-9, 9]. A duplicate, a non-integer, or an out-of-range value is present but
// not ok, which §4.1 requires the caller to refuse with 501.
func mtPriorityValid(arg string) (present, ok bool) {
	_, after, found := strings.Cut(arg, ">")
	if !found {
		return false, true
	}
	count := 0
	var raw string
	for f := range strings.FieldsSeq(after) {
		k, v, _ := strings.Cut(f, "=")
		if strings.EqualFold(k, "MT-PRIORITY") {
			count++
			raw = v
		}
	}
	if count == 0 {
		return false, true
	}
	if count > 1 {
		return true, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < -9 || n > 9 {
		return true, false
	}
	return true, true
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

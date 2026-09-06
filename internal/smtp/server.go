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

// MailParams carries the validated ESMTP MAIL FROM parameters the backend acts
// on. RET and ENVID are the RFC 3461 DSN parameters (RET is "FULL" or "HDRS"
// upper-cased, or empty; ENVID is the sender's envelope identifier as received,
// still xtext-encoded). Empty means the sender supplied none.
type MailParams struct {
	RET   string
	ENVID string
}

// RcptParams carries the validated ESMTP RCPT TO parameters the backend acts on.
// Notify and ORCPT are the RFC 3461 per-recipient DSN parameters (Notify as
// received, e.g. "NEVER" or "SUCCESS,FAILURE"; ORCPT as the "addr-type;xtext"
// original-recipient value). Empty means the sender supplied none.
type RcptParams struct {
	Notify string
	ORCPT  string
}

// Session carries one connection's state through its mail transactions. Mail
// begins a transaction, Rcpt adds a recipient, Data consumes the message body,
// Reset abandons the current transaction, and Logout is called once as the
// connection closes. Mail and Rcpt receive the validated ESMTP parameters for
// that command.
type Session interface {
	Mail(from string, params MailParams) error
	Rcpt(to string, params RcptParams) error
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

// smtpConn is one connection's state: everything a command handler reads or
// writes. STARTTLS replaces conn, w and tp and flips tls, so every handler takes
// a pointer receiver and every write goes through c.w rather than a captured
// writer.
type smtpConn struct {
	srv     *Server
	conn    net.Conn
	w       *ew
	tp      *textproto.Reader
	tls     bool
	remote  string
	sess    Session
	canAuth bool

	// greeted records that the client has sent HELO/EHLO. RFC 5321 §4.1.4: a
	// session carrying mail transactions MUST first be initialized by EHLO, so
	// MAIL before a greeting is a 503. Non-mail commands (VRFY/EXPN/HELP) are
	// accepted without it. STARTTLS clears it (the client re-issues EHLO).
	greeted bool
	// helo is trace context for the Received: header stamped at DATA time: the
	// HELO/EHLO argument names the connecting client. It is cleared by STARTTLS,
	// which discards all prior session state (RFC 3207).
	helo      string
	hasFrom   bool
	rcptCount int
	// binaryMIME records a BODY=BINARYMIME MAIL parameter (RFC 3030), which
	// mandates BDAT rather than DATA for the body.
	binaryMIME bool
	// smtputf8 records that this transaction's MAIL carried the SMTPUTF8 keyword
	// (RFC 6531). It is what permits a non-ASCII address on this transaction's
	// RCPT commands, since SMTPUTF8 is negotiated once on MAIL and has no RCPT
	// counterpart.
	smtputf8 bool
	// bdatBuf accumulates the BDAT chunks of the current transaction (nil until
	// the first chunk); bdatErr marks a chunk that failed (size exceeded), so
	// further chunks are drained and dropped until RSET clears the transaction.
	bdatBuf *bytes.Buffer
	bdatErr bool
}

// commandTable maps an upper-cased SMTP verb to its handler. A handler returns
// false to abandon the connection; true continues the loop. It is the single
// source of dispatch: adding a verb here is the whole wiring, and a verb absent
// from it falls through to the 500 an unknown command gets.
//
// The HELP reply lists the supported verbs by hand rather than from this table,
// because a handler reading the table Go initializes from that handler is an
// initialization cycle the compiler refuses.
var commandTable = map[string]func(*smtpConn, string) bool{
	"HELO":     (*smtpConn).cmdHELO,
	"EHLO":     (*smtpConn).cmdEHLO,
	"AUTH":     (*smtpConn).cmdAUTH,
	"STARTTLS": (*smtpConn).cmdSTARTTLS,
	"MAIL":     (*smtpConn).cmdMAIL,
	"RCPT":     (*smtpConn).cmdRCPT,
	"DATA":     (*smtpConn).cmdDATA,
	"BDAT":     (*smtpConn).cmdBDAT,
	"RSET":     (*smtpConn).cmdRSET,
	"NOOP":     (*smtpConn).cmdNOOP,
	"QUIT":     (*smtpConn).cmdQUIT,
	"VRFY":     (*smtpConn).cmdVRFY,
	"EXPN":     (*smtpConn).cmdEXPN,
	"HELP":     (*smtpConn).cmdHELP,
}

func (s *Server) handle(conn net.Conn) {
	remote := conn.RemoteAddr().String()
	sess, err := s.Backend.NewSession(remote)
	if err != nil {
		s.Logger.Emit(logging.Event{
			Level: logging.LevelWarn, Subsystem: logging.SMTP, Name: "conn.reject",
			RemoteAddr: remote, Fields: logging.Fields{"reason": err.Error()},
		})
		reply(&ew{out: bufio.NewWriter(conn)}, 421, s.hostname()+" service not available")
		_ = conn.Close()
		return
	}
	// A session that can validate credentials enables AUTH, but only over TLS,
	// so the EHLO advertisement is also gated on the link being secured.
	_, canAuth := sess.(Authenticator)
	_, isTLS := conn.(*tls.Conn)
	c := &smtpConn{
		srv:     s,
		conn:    conn,
		w:       &ew{out: bufio.NewWriter(conn)},
		tp:      textproto.NewReader(bufio.NewReader(conn)),
		tls:     isTLS,
		remote:  remote,
		sess:    sess,
		canAuth: canAuth,
	}
	defer func() { _ = c.conn.Close() }() // closes the upgraded conn after a STARTTLS swap
	defer sess.Logout()

	c.reply(220, s.hostname()+" ESMTP hermEX")
	c.event(logging.LevelInfo, "conn.accept", logging.Fields{"tls": isTLS})
	c.loop()
}

// loop reads and dispatches commands until the client quits, the link fails, or
// a handler abandons the connection.
func (c *smtpConn) loop() {
	for {
		line, err := readCommandLine(c.tp.R)
		if errors.Is(err, errLineTooLong) {
			c.reply(500, "5.5.2 line too long")
			continue
		}
		if err != nil {
			return
		}
		if c.w.err != nil {
			return // a prior reply failed to reach the client; the link is gone
		}
		cmd, arg, _ := strings.Cut(line, " ")
		verb := strings.ToUpper(cmd)
		c.event(logging.LevelDebug, "command", logging.Fields{"cmd": verb})
		run, known := commandTable[verb]
		if !known {
			c.reply(500, "command not recognized")
			continue
		}
		if !run(c, arg) {
			return
		}
	}
}

// reply writes one SMTP reply through the connection's current writer, which
// STARTTLS replaces.
func (c *smtpConn) reply(code int, msg string) { reply(c.w, code, msg) }

// event logs through the server's logger, tagged with the client address. SMTP
// intake has no authenticated user, so the envelope sender goes in Fields, not
// the User column. A nil logger is a no-op.
func (c *smtpConn) event(level logging.Level, name string, f logging.Fields) {
	c.srv.Logger.Emit(logging.Event{
		Level: level, Subsystem: logging.SMTP, Name: name, RemoteAddr: c.remote, Fields: f,
	})
}

// logInternal records an error the wire reply deliberately withholds, so a
// sanitized rejection is diagnosable rather than silent.
func (c *smtpConn) logInternal(err error) {
	c.event(logging.LevelError, "session.error", logging.Fields{"reason": err.Error()})
}

// resetTxn clears all envelope and body state at a transaction boundary (a new
// MAIL, RSET, a completed DATA/BDAT, or a re-greeting), leaving the greeting
// (greeted/helo) untouched.
func (c *smtpConn) resetTxn() {
	c.hasFrom, c.rcptCount, c.binaryMIME, c.smtputf8 = false, 0, false, false
	c.bdatBuf, c.bdatErr = nil, false
}

func (c *smtpConn) cmdHELO(arg string) bool {
	c.resetTxn()
	c.greeted, c.helo = true, arg
	c.sess.Reset()
	c.reply(250, c.srv.hostname())
	return true
}

func (c *smtpConn) cmdEHLO(arg string) bool {
	c.resetTxn()
	c.greeted, c.helo = true, arg
	c.sess.Reset()
	c.srv.greetEHLO(c.w, arg, c.tls, c.canAuth && c.tls)
	return true
}

func (c *smtpConn) cmdAUTH(arg string) bool {
	c.srv.handleAuth(c.w, c.tp, arg, c.sess, c.tls, c.canAuth)
	return true
}

func (c *smtpConn) cmdSTARTTLS(string) bool {
	if c.srv.TLSConfig == nil || c.tls {
		c.reply(502, "STARTTLS not available")
		return true
	}
	if c.tp.R.Buffered() > 0 {
		c.event(logging.LevelWarn, "starttls.injection", nil)
		return false // pipelined plaintext behind STARTTLS; abort the connection
	}
	c.reply(220, "ready to start TLS")
	tc := tls.Server(c.conn, c.srv.TLSConfig)
	if err := tc.Handshake(); err != nil {
		return false // handshake failed; the deferred close fires
	}
	c.conn = tc
	c.w = &ew{out: bufio.NewWriter(tc)}
	c.tp = textproto.NewReader(bufio.NewReader(tc))
	c.tls = true
	// RFC 3207: discard all state negotiated before TLS; the client re-issues
	// EHLO over the secured link.
	c.sess.Reset()
	c.resetTxn()
	c.greeted, c.helo = false, ""
	c.event(logging.LevelInfo, "starttls", nil)
	return true
}

func (c *smtpConn) cmdMAIL(arg string) bool {
	if !c.greeted {
		c.reply(503, "5.5.1 send HELO/EHLO first")
		return true
	}
	addr, ok := extractPath(arg, "FROM:")
	if !ok {
		c.reply(501, "syntax: MAIL FROM:<address>")
		return true
	}
	params := esmtpParams(arg)
	// RFC 6531 §3.5: a non-ASCII address is only permitted once the client has
	// asked for the extension on this MAIL. Accepting one without it would let an
	// address this server cannot promise to relay enter the queue, since the
	// SMTPUTF8 requirement is negotiated per transaction and never re-derived
	// downstream.
	_, wantsUTF8 := params["SMTPUTF8"]
	if needsUTF8Keyword(addr, wantsUTF8) {
		c.reply(550, "5.6.7 non-ASCII address requires the SMTPUTF8 extension")
		return true
	}
	mailDSN, code, msg, ok := c.checkMailParams(arg, params)
	if !ok {
		c.reply(code, msg)
		return true
	}
	if err := c.sess.Mail(addr, mailDSN); err != nil {
		replySessionErr(c.w, err, c.logInternal)
		return true
	}
	c.resetTxn()
	c.hasFrom = true
	// RFC 3030: BODY=BINARYMIME commits the sender to delivering the body over
	// BDAT; a later DATA in this transaction is then a sequence error.
	c.binaryMIME = strings.EqualFold(params["BODY"], "BINARYMIME")
	c.smtputf8 = wantsUTF8
	c.event(logging.LevelInfo, "mail.from", logging.Fields{"from": addr})
	c.reply(250, "OK")
	return true
}

// checkMailParams validates the ESMTP parameters that can refuse a MAIL before
// the backend is consulted. It returns the DSN parameters to hand the session,
// or ok=false with the reply the refusal earns.
func (c *smtpConn) checkMailParams(arg string, params map[string]string) (MailParams, int, string, bool) {
	// RFC 1870: when the client declares SIZE and it exceeds the advertised
	// maximum, refuse the whole transaction now with 552 rather than accepting
	// MAIL/RCPT and streaming the body only to reject it after the bytes have
	// crossed the wire.
	if limit := c.srv.maxSize.Load(); limit > 0 {
		if sz, ok := declaredSize(params); ok && sz > limit {
			return MailParams{}, 552, "5.3.4 message size exceeds limit", false
		}
	}
	// RFC 6710 §4.1: a present-but-invalid MT-PRIORITY (malformed, out of the
	// -9..9 range, or duplicated) MUST be refused. The value itself is otherwise
	// unused: this MTA applies the default priority policy (all messages at
	// priority 0), which also satisfies the rule that an untrusted sender MUST NOT
	// upgrade a message's priority.
	if present, ok := mtPriorityValid(arg); present && !ok {
		return MailParams{}, 501, "5.5.2 syntax error in MT-PRIORITY parameter", false
	}
	// RFC 3461 §5.1(a): a malformed or duplicated RET/ENVID MUST be refused with
	// 501; a valid one never changes the reply this MAIL would otherwise get.
	mailDSN, ok := dsnMailParams(arg)
	if !ok {
		return MailParams{}, 501, "5.5.4 syntax error in DSN parameter", false
	}
	return mailDSN, 0, "", true
}

func (c *smtpConn) cmdRCPT(arg string) bool {
	if !c.hasFrom {
		c.reply(503, "need MAIL before RCPT")
		return true
	}
	addr, ok := extractPath(arg, "TO:")
	if !ok {
		c.reply(501, "syntax: RCPT TO:<address>")
		return true
	}
	// RFC 6531 §3.5: SMTPUTF8 is negotiated on MAIL, so a non-ASCII recipient
	// belongs to a transaction that asked for it. 553 is the recipient-side code
	// (550 is the sender's).
	if needsUTF8Keyword(addr, c.smtputf8) {
		c.reply(553, "5.6.7 non-ASCII address requires the SMTPUTF8 extension")
		return true
	}
	// RFC 3461 §5.1(b): a malformed or duplicated NOTIFY/ORCPT MUST be refused
	// with 501; a valid one never changes the reply this RCPT would otherwise get.
	rcptDSN, ok := dsnRcptParams(arg)
	if !ok {
		c.reply(501, "5.5.4 syntax error in DSN parameter")
		return true
	}
	if err := c.sess.Rcpt(addr, rcptDSN); err != nil {
		replySessionErr(c.w, err, c.logInternal)
		return true
	}
	c.rcptCount++
	c.event(logging.LevelInfo, "rcpt.to", logging.Fields{"to": addr})
	c.reply(250, "OK")
	return true
}

func (c *smtpConn) cmdDATA(string) bool {
	// RFC 3030: DATA and BDAT cannot mix in one transaction, and a BINARYMIME
	// body must arrive via BDAT; both are 503 sequence errors.
	switch {
	case c.bdatBuf != nil:
		c.reply(503, "5.5.1 DATA not allowed after BDAT; send RSET")
		return true
	case c.binaryMIME:
		c.reply(503, "5.5.1 BINARYMIME requires BDAT, not DATA")
		return true
	case c.rcptCount == 0:
		c.reply(503, "need RCPT before DATA")
		return true
	}
	c.reply(354, "end data with <CR><LF>.<CR><LF>")
	c.reportBody(c.srv.consumeData(c.tp, c.sess, c.trace()))
	c.resetTxn()
	return true
}

// trace builds the Received: header stamped on the body this transaction is
// about to accept.
func (c *smtpConn) trace() string {
	return buildReceived(c.helo, c.remote, lookupRDNS(c.remote), c.srv.hostname(), c.tls, time.Now())
}

// reportBody logs and answers the outcome of a body the session consumed, the
// one place DATA and BDAT agree on.
func (c *smtpConn) reportBody(err error) {
	if err != nil {
		c.event(logging.LevelWarn, "message.reject", logging.Fields{"recipients": c.rcptCount, "reason": err.Error()})
		replyDataErr(c.w, err)
		return
	}
	c.event(logging.LevelInfo, "message.accept", logging.Fields{"recipients": c.rcptCount})
	c.reply(250, "OK")
}

// cmdBDAT implements RFC 3030 CHUNKING: "BDAT <chunk-size> [LAST]". The chunk's
// octets follow the command line's CRLF directly, with no dot-stuffing and no
// "." terminator; the receiver reads exactly chunk-size octets.
func (c *smtpConn) cmdBDAT(arg string) bool {
	size, last, ok := parseBDAT(arg)
	if !ok {
		// Without a valid octet count the chunk cannot be framed, so the stream
		// position is unknown; refuse rather than guess.
		c.reply(501, "5.5.4 syntax: BDAT <chunk-size> [LAST]")
		return true
	}
	limit := c.srv.maxSize.Load()
	if !c.readChunk(size, limit) {
		return false
	}
	c.replyBDAT(size, last, limit)
	return true
}

// readChunk takes the chunk off the wire before any reply is sent (RFC 3030: a
// failure "MUST accept and discard the associated message data before sending
// the appropriate 5XX or 4XX code"), or the next command read would parse
// message bytes. It reports false when the chunk was truncated, which leaves the
// stream unframed and the connection unusable.
func (c *smtpConn) readChunk(size, limit int64) bool {
	if !c.chunkViable() {
		_, err := io.CopyN(io.Discard, c.tp.R, size)
		return err == nil
	}
	return c.bufferChunk(size, limit)
}

// chunkViable reports whether the transaction can accept this chunk at all.
func (c *smtpConn) chunkViable() bool {
	return c.greeted && c.rcptCount > 0 && !c.bdatErr
}

// bufferChunk accumulates the chunk, capped at the size limit so an oversized
// declared chunk cannot exhaust memory (OWASP A05); the remainder is drained.
func (c *smtpConn) bufferChunk(size, limit int64) bool {
	if c.bdatBuf == nil {
		c.bdatBuf = new(bytes.Buffer)
	}
	toBuf := size
	if limit > 0 {
		toBuf = min(toBuf, max(limit+1-int64(c.bdatBuf.Len()), 0))
	}
	if _, err := io.CopyN(c.bdatBuf, c.tp.R, toBuf); err != nil {
		return false // truncated chunk; the stream is no longer framed
	}
	_, err := io.CopyN(io.Discard, c.tp.R, size-toBuf)
	return err == nil
}

// replyBDAT answers a chunk that has already been read off the wire.
func (c *smtpConn) replyBDAT(size int64, last bool, limit int64) {
	switch {
	case !c.greeted:
		c.reply(503, "5.5.1 send HELO/EHLO first")
	case c.rcptCount == 0:
		c.reply(503, "5.5.1 need MAIL and RCPT before BDAT")
	case c.bdatErr:
		c.reply(503, "5.5.0 BDAT transaction failed; send RSET")
	case limit > 0 && int64(c.bdatBuf.Len()) > limit:
		c.bdatErr = true // poison the transaction; later chunks drain until RSET
		c.reply(552, "5.3.4 message size exceeds limit")
	case !last:
		c.reply(250, fmt.Sprintf("2.0.0 %d octets received", size))
	default:
		body := io.MultiReader(strings.NewReader(c.trace()), bytes.NewReader(c.bdatBuf.Bytes()))
		c.reportBody(c.sess.Data(body))
		c.resetTxn()
	}
}

func (c *smtpConn) cmdRSET(string) bool {
	c.sess.Reset()
	c.resetTxn()
	c.reply(250, "OK")
	return true
}

func (c *smtpConn) cmdNOOP(string) bool {
	c.reply(250, "OK")
	return true
}

func (c *smtpConn) cmdQUIT(string) bool {
	c.reply(221, c.srv.hostname()+" closing connection")
	return false
}

// cmdVRFY answers RFC 5321 §3.5.1/§7.3: never confirm or deny a specific address
// (that is user enumeration). The privacy-preserving 252 promises only to accept
// and attempt delivery; a 250 or 550 would leak whether the mailbox exists.
func (c *smtpConn) cmdVRFY(string) bool {
	c.reply(252, "2.1.5 Cannot VRFY user, but will accept message and attempt delivery")
	return true
}

// cmdEXPN answers RFC 5321 §3.5.2/§7.3: mailing-list expansion is disabled (an
// address-harvesting vector); 502 marks it recognized but not implemented
// (§4.2.4), not the 500 of an unknown command.
func (c *smtpConn) cmdEXPN(string) bool {
	c.reply(502, "5.5.1 EXPN not available")
	return true
}

// cmdHELP answers RFC 5321 §4.1.1.8 with a 214, recognized rather than 500.
func (c *smtpConn) cmdHELP(string) bool {
	c.reply(214, "2.0.0 hermEX ESMTP; supported: HELO EHLO MAIL RCPT DATA BDAT RSET NOOP QUIT (RFC 5321)")
	return true
}

var errTooLarge = errors.New("message too large")

// TempError is a Session error the server reports as a temporary failure (a 4xx),
// so the sending MTA retries later, rather than a permanent rejection (a 5xx).
// Greylisting returns it from Rcpt to defer a first-contact triplet.
type TempError struct{ Message string }

func (e *TempError) Error() string { return e.Message }

// PermError is the permanent counterpart of TempError: a Session error the server
// reports as a 5xx, and whose Message is written to the wire. Only these two types
// put text on the wire. Everything else is answered with a fixed string, because
// port 25 takes mail from unauthenticated peers and the errors reaching these
// helpers are not all hand-written: a store or driver failure carries the mailbox
// path on disk or names database internals. Wrapping a business rejection in a
// PermError is what marks its message as safe to disclose.
type PermError struct{ Message string }

func (e *PermError) Error() string { return e.Message }

// replySessionErr maps a Session error to its SMTP reply: a TempError becomes a 451
// temporary failure (the sender retries), a PermError a 550 carrying its own message,
// and anything else a 550 with a fixed string, its real error passed to logErr for
// the server-side record.
// ew wraps the response bufio.Writer and records the first write error, so the
// reply helpers stay linear; the command loop checks err once per command and
// abandons the connection when a reply has failed to reach the client.
type ew struct {
	out *bufio.Writer
	err error
}

func (e *ew) printf(format string, a ...any) {
	if e.err == nil {
		_, e.err = fmt.Fprintf(e.out, format, a...)
	}
}

func (e *ew) flush() {
	if e.err == nil {
		e.err = e.out.Flush()
	}
}

func replySessionErr(w *ew, err error, logErr func(error)) {
	switch {
	case isTempErr(err):
		reply(w, 451, tempMessage(err))
	case isPermErr(err):
		reply(w, 550, permMessage(err))
	default:
		logErr(err)
		reply(w, 550, "5.3.0 recipient rejected")
	}
}

// replyDataErr maps a Session.Data error to the SMTP reply shared by the DATA and
// BDAT body paths: a TempError defers (451 so the sender retries), an over-size body
// is 552, a PermError is a 554 carrying its own message, and any other failure is a
// 554 with a fixed string. The DATA path already records the error, so this one
// takes no logErr.
func replyDataErr(w *ew, err error) {
	switch {
	case isTempErr(err):
		reply(w, 451, tempMessage(err))
	case errors.Is(err, errTooLarge):
		reply(w, 552, "message exceeds size limit")
	case isPermErr(err):
		reply(w, 554, permMessage(err))
	default:
		reply(w, 554, "5.3.0 transaction failed")
	}
}

func isTempErr(err error) bool { _, ok := errors.AsType[*TempError](err); return ok }
func isPermErr(err error) bool { _, ok := errors.AsType[*PermError](err); return ok }

func tempMessage(err error) string {
	te, _ := errors.AsType[*TempError](err)
	return te.Message
}

func permMessage(err error) string {
	pe, _ := errors.AsType[*PermError](err)
	return pe.Message
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
	_, _ = io.Copy(io.Discard, dot)
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

func (s *Server) greetEHLO(w *ew, arg string, isTLS, authAvailable bool) {
	lines := []string{
		fmt.Sprintf("%s Hello %s", s.hostname(), strings.TrimSpace(arg)),
		"PIPELINING",
		"8BITMIME",
		"ENHANCEDSTATUSCODES",
		"SMTPUTF8",
		"CHUNKING",
		"BINARYMIME",
		"MT-PRIORITY",
		"DSN",
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
		w.printf("250%s%s\r\n", sep, l)
	}
	w.flush()
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
func reply(w *ew, code int, msg string) {
	if enh := defaultEnhanced(code); enh != "" && !startsWithEnhanced(msg) {
		msg = enh + " " + msg
	}
	w.printf("%d %s\r\n", code, msg)
	w.flush()
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

// needsUTF8Keyword reports whether an envelope address must be refused: it
// carries a byte outside ASCII, which only the SMTPUTF8 extension permits
// (RFC 6531 §3.2), and the transaction did not ask for it. An empty
// reverse-path (the null sender) is ASCII and always passes.
func needsUTF8Keyword(addr string, negotiated bool) bool {
	if negotiated {
		return false
	}
	for i := range len(addr) {
		if addr[i] > 0x7f {
			return true
		}
	}
	return false
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

// dsnMailParams parses and validates the RFC 3461 RET and ENVID parameters of a
// MAIL FROM argument. ok is false when a value is malformed or either parameter
// appears more than once, which §5.1(a) requires the caller to refuse with 501.
// An absent parameter yields an empty field and ok=true.
func dsnMailParams(arg string) (p MailParams, ok bool) {
	_, after, found := strings.Cut(arg, ">")
	if !found {
		return MailParams{}, true
	}
	var retN, envidN int
	for f := range strings.FieldsSeq(after) {
		k, v, _ := strings.Cut(f, "=")
		switch strings.ToUpper(k) {
		case "RET":
			retN++
			up := strings.ToUpper(v)
			if up != "FULL" && up != "HDRS" {
				return MailParams{}, false
			}
			p.RET = up
		case "ENVID":
			envidN++
			if len(v) > 100 || !validXtext(v) {
				return MailParams{}, false
			}
			p.ENVID = v
		}
	}
	if retN > 1 || envidN > 1 {
		return MailParams{}, false
	}
	return p, true
}

// dsnRcptParams parses and validates the RFC 3461 NOTIFY and ORCPT parameters of a
// RCPT TO argument. ok is false when a value is malformed or either parameter
// appears more than once, which §5.1(b) requires the caller to refuse with 501.
// An absent parameter yields an empty field and ok=true.
func dsnRcptParams(arg string) (p RcptParams, ok bool) {
	_, after, found := strings.Cut(arg, ">")
	if !found {
		return RcptParams{}, true
	}
	var notifyN, orcptN int
	for f := range strings.FieldsSeq(after) {
		k, v, _ := strings.Cut(f, "=")
		switch strings.ToUpper(k) {
		case "NOTIFY":
			notifyN++
			if !validNotify(v) {
				return RcptParams{}, false
			}
			p.Notify = v
		case "ORCPT":
			orcptN++
			if len(v) > 500 || !validORCPT(v) {
				return RcptParams{}, false
			}
			p.ORCPT = v
		}
	}
	if notifyN > 1 || orcptN > 1 {
		return RcptParams{}, false
	}
	return p, true
}

// validNotify reports whether v is a valid RFC 3461 NOTIFY value: either "NEVER"
// by itself, or a comma-separated list of one or more of SUCCESS, FAILURE, and
// DELAY. NEVER MUST NOT be combined with any other keyword, and an empty value is
// invalid. Matching is case-insensitive.
func validNotify(v string) bool {
	if v == "" {
		return false
	}
	if strings.EqualFold(v, "NEVER") {
		return true
	}
	for elem := range strings.SplitSeq(v, ",") {
		switch strings.ToUpper(elem) {
		case "SUCCESS", "FAILURE", "DELAY":
		default:
			return false
		}
	}
	return true
}

// validORCPT reports whether v is a valid RFC 3461 ORCPT value of the form
// "addr-type;xtext", where addr-type is a non-empty RFC 822 atom and the
// remainder is xtext-encoded.
func validORCPT(v string) bool {
	atype, xt, found := strings.Cut(v, ";")
	if !found || !isAtom(atype) {
		return false
	}
	return validXtext(xt)
}

// validXtext reports whether s conforms to the RFC 3461 §4 xtext grammar: each
// unit is either a printable US-ASCII character in [33,126] other than "+" and
// "=", or a "+" followed by two upper-case hexadecimal digits.
func validXtext(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '+':
			if i+2 >= len(s) || !isHexUpper(s[i+1]) || !isHexUpper(s[i+2]) {
				return false
			}
			i += 2
		case c >= '!' && c <= '~' && c != '=':
			// a bare xchar
		default:
			return false
		}
	}
	return true
}

// isHexUpper reports whether c is a digit or an upper-case hexadecimal letter
// (A-F), the only forms RFC 3461 §4 permits after a "+" in xtext.
func isHexUpper(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')
}

// isAtom reports whether s is a non-empty RFC 822 atom: one or more characters,
// none of them a space, a control, or an RFC 822 "special".
func isAtom(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c >= 0x7f {
			return false
		}
		switch c {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=':
			return false
		}
	}
	return true
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

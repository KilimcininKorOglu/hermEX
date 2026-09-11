// Package pop3 implements an RFC 1939 POP3 retrieval server backed by the mailbox
// store, with the CAPA extension mechanism (RFC 2449), STLS (RFC 2595), SASL AUTH
// PLAIN/LOGIN (RFC 5034), and UTF8 + LANG (RFC 6856). It authenticates with
// USER/PASS or AUTH through a directory.Authenticator and serves a login-time
// snapshot of the INBOX.
package pop3

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"

	"hermex/internal/authlimit"
	"hermex/internal/connlimit"
	"hermex/internal/directory"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/netline"
	"hermex/internal/objectstore"
)

// Server accepts POP3 connections and serves mailboxes resolved via Auth.
type Server struct {
	Auth      directory.Authenticator
	Hostname  string
	TLSConfig *tls.Config        // when non-nil, advertise (CAPA) and accept STLS
	Logger    *logging.Logger    // central activity log; nil disables logging
	Limiter   *authlimit.Limiter // failed-login throttle keyed by client IP; nil disables it

	// maxCommandLine is the cap on ONE line read from the client in bytes (0 = the
	// built-in defaultMaxCommandLine), held atomically so the daemon's poll can
	// apply an operator's edit while connections run, with no restart.
	maxCommandLine atomic.Int64

	conns lifecycle.ConnGroup
}

// defaultMaxCommandLine caps one line read from the client. A POP3 command is short
// (RFC 1939), but a SASL continuation carries a base64 token, so the default leaves
// room for one. Without a cap a client that never sends a line terminator grows the
// daemon's memory without limit, and it reaches this reader before it authenticates.
const defaultMaxCommandLine = 8 << 10 // 8 KiB

// SetMaxCommandLine sets the maximum accepted line in bytes (0 restores the
// built-in default). It is safe to call concurrently with active connections, so an
// operator's edit applies without a restart.
func (s *Server) SetMaxCommandLine(n int64) {
	if n < 0 {
		n = 0
	}
	s.maxCommandLine.Store(n)
}

// commandLineLimit is the line cap in force right now.
func (s *Server) commandLineLimit() int {
	if n := s.maxCommandLine.Load(); n > 0 {
		return int(n)
	}
	return defaultMaxCommandLine
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

// SetConnLimiter caps how many connections this daemon serves at once, in total
// and per client address. A refused connection is told with an -ERR greeting, the
// refusal RFC 1939 gives a server that will not serve the connection, and then
// closed. A nil limiter leaves the server uncapped.
func (s *Server) SetConnLimiter(l *connlimit.Limiter) {
	if l == nil {
		return
	}
	gate, refuse := connlimit.Gate(l, "-ERR too many connections\r\n", s.logConnRefused)
	s.conns.SetGate(gate, refuse)
}

// logConnRefused records a connection the cap refused, naming which cap it was so
// an operator can tell a full daemon from one busy client.
func (s *Server) logConnRefused(remote string, why connlimit.Reason) {
	s.Logger.Emit(logging.Event{
		Level:      logging.LevelWarn,
		Subsystem:  logging.POP3,
		Name:       "conn.refused",
		RemoteAddr: remote,
		Fields:     logging.Fields{"cap": string(why)},
	})
}

// ew wraps the response bufio.Writer and records the first write error. The POP3
// response helpers stay linear (no error return threaded through every line), and
// handle checks err once per command: a failed write to the client means the
// connection is gone, so the session is abandoned.
type ew struct {
	out *bufio.Writer
	err error
}

func (e *ew) str(s string) {
	if e.err == nil {
		_, e.err = e.out.WriteString(s)
	}
}

func (e *ew) wbyte(b byte) {
	if e.err == nil {
		e.err = e.out.WriteByte(b)
	}
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

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }() // closes the upgraded conn after an STLS swap
	_, isTLS := conn.(*tls.Conn)
	c := &pop3Session{
		s:     s,
		conn:  conn,
		w:     &ew{out: bufio.NewWriter(conn)},
		tp:    textproto.NewReader(bufio.NewReader(conn)),
		isTLS: isTLS,
	}
	defer c.closeMailbox()

	ok(c.w, "hermEX POP3 ready")
	c.event(logging.LevelInfo, "conn.accept", logging.Fields{"tls": isTLS})
	c.serve()
}

// pop3Session is one connection's state. The writer, reader and TLS flag change
// mid-session on an STLS upgrade, and the user changes on login.
type pop3Session struct {
	s     *Server
	conn  net.Conn
	w     *ew
	tp    *textproto.Reader
	isTLS bool
	user  string
	mb    *mailbox // nil until authenticated: the AUTHORIZATION/TRANSACTION split
}

// event logs through the server's logger, reading the session's live user and
// connection. A nil logger is a no-op.
func (c *pop3Session) event(level logging.Level, name string, f logging.Fields) {
	c.s.Logger.Emit(logging.Event{
		Level:      level,
		Subsystem:  logging.POP3,
		Name:       name,
		User:       c.user,
		RemoteAddr: c.conn.RemoteAddr().String(),
		Fields:     f,
	})
}

// closeMailbox releases the snapshot the session opened, if any.
func (c *pop3Session) closeMailbox() {
	if c.mb != nil {
		_ = c.mb.st.Close()
	}
}

// serve reads and runs commands until the client quits or the link fails.
func (c *pop3Session) serve() {
	for {
		line, err := netline.ReadLine(c.tp.R, c.s.commandLineLimit())
		if errors.Is(err, netline.ErrTooLong) {
			// The rest of the line is already discarded, so the connection is at a
			// line boundary: tell the client and keep serving.
			c.event(logging.LevelWarn, "command.too-long", logging.Fields{"limit": c.s.commandLineLimit()})
			errLine(c.w, "command line too long")
			if c.w.err != nil {
				return
			}
			continue
		}
		if err != nil {
			return // client gone; per RFC no deletions are committed
		}
		if c.w.err != nil {
			return // a prior response failed to reach the client; the link is gone
		}
		cmd, arg, _ := strings.Cut(line, " ")
		cmd = strings.ToUpper(cmd)

		// Per-command audit at debug level, the verb only, never the argument
		// (PASS's argument is the password).
		c.event(logging.LevelDebug, "command", logging.Fields{"cmd": cmd})
		if c.command(cmd, arg) {
			return
		}
	}
}

// command runs one command, reporting whether the session ends here.
func (c *pop3Session) command(cmd, arg string) (done bool) {
	// CAPA (RFC 2449) and LANG (RFC 6856) are valid in both the AUTHORIZATION
	// and TRANSACTION states, so they are answered before the state split.
	switch cmd {
	case "CAPA":
		c.s.writeCapa(c.w, c.isTLS)
		return false
	case "LANG":
		writeLang(c.w, arg)
		return false
	}
	if c.mb == nil {
		return c.authCommand(cmd, arg)
	}
	return c.transactionCommand(cmd, arg)
}

// authCommand runs an AUTHORIZATION-state command.
func (c *pop3Session) authCommand(cmd, arg string) (done bool) {
	switch cmd {
	case "USER":
		c.user = arg
		ok(c.w, "")
	case "PASS":
		c.finishPass(arg)
	case "AUTH":
		c.finishSASL(arg)
	case "UTF8":
		// RFC 6856: enter UTF-8 mode (valid only in AUTHORIZATION). hermEX
		// serves the stored message bytes verbatim and never downgrades, so
		// this is an acknowledgment with no behavior change.
		ok(c.w, "UTF-8 mode enabled")
		c.event(logging.LevelInfo, "utf8", nil)
	case "STLS":
		return c.startTLS()
	case "QUIT":
		ok(c.w, "bye")
		return true
	default:
		errLine(c.w, "command not allowed before authentication")
	}
	return false
}

// finishPass completes a USER/PASS login. A refusal discards the claimed user, so
// the next attempt starts clean.
func (c *pop3Session) finishPass(pass string) {
	m, okAuth := c.s.finishAuth(c.w, c.conn, c.user, pass)
	if !okAuth {
		c.user = ""
		return
	}
	c.mb = m
}

// finishSASL completes an AUTH login (RFC 5034): PLAIN and LOGIN, both password
// mechanisms that reuse the same Authenticate + privilege gate as USER/PASS.
func (c *pop3Session) finishSASL(arg string) {
	authUser, m, okAuth := c.s.authSASL(c.w, c.tp, c.conn, arg)
	if !okAuth {
		return
	}
	c.user = authUser
	c.mb = m
}

// startTLS upgrades the connection in place (RFC 2595), reporting whether the
// session must end.
func (c *pop3Session) startTLS() (done bool) {
	if c.s.TLSConfig == nil || c.isTLS {
		errLine(c.w, "STLS not available")
		return false
	}
	if c.tp.R.Buffered() > 0 {
		c.event(logging.LevelWarn, "stls.injection", nil)
		return true // pipelined plaintext behind STLS; abort the connection
	}
	ok(c.w, "begin TLS negotiation")
	tc := tls.Server(c.conn, c.s.TLSConfig)
	if err := tc.Handshake(); err != nil {
		return true // handshake failed; deferred close fires
	}
	c.conn = tc
	c.w = &ew{out: bufio.NewWriter(tc)}
	c.tp = textproto.NewReader(bufio.NewReader(tc))
	c.isTLS = true
	c.user = "" // discard any USER given before TLS
	c.event(logging.LevelInfo, "stls", nil)
	return false
}

// transactionCommands are the TRANSACTION-state commands that act on the mailbox
// snapshot and leave the session open.
var transactionCommands = map[string]func(c *pop3Session, arg string){
	"STAT": func(c *pop3Session, _ string) { ok(c.w, fmt.Sprintf("%d %d", c.mb.count(), c.mb.totalSize())) },
	"LIST": func(c *pop3Session, arg string) { c.mb.list(c.w, arg, false) },
	"UIDL": func(c *pop3Session, arg string) { c.mb.list(c.w, arg, true) },
	"TOP":  func(c *pop3Session, arg string) { c.mb.top(c.w, arg) },
	"RETR": func(c *pop3Session, arg string) { c.mb.retr(c.w, arg) },
	"DELE": func(c *pop3Session, arg string) { c.mb.dele(c.w, arg) },
	"RSET": func(c *pop3Session, _ string) { c.mb.reset(); ok(c.w, "") },
	"NOOP": func(c *pop3Session, _ string) { ok(c.w, "") },
}

// transactionCommand runs a TRANSACTION-state command, reporting whether the
// session ends here (QUIT, which is also where deletions are committed).
func (c *pop3Session) transactionCommand(cmd, arg string) (done bool) {
	if cmd == "QUIT" {
		c.quit()
		return true
	}
	run, known := transactionCommands[cmd]
	if !known {
		errLine(c.w, "unknown command")
		return false
	}
	run(c, arg)
	return false
}

// quit commits the session's deletions and says goodbye. A commit failure is
// recorded rather than hidden; the client is told bye either way.
func (c *pop3Session) quit() {
	if failed := c.mb.commit(); len(failed) > 0 {
		c.event(logging.LevelError, "quit.commit.fail", logging.Fields{
			"folder": c.mb.folder,
			"uids":   failed,
		})
	}
	ok(c.w, "bye")
}

// mailbox is a login-time snapshot of a folder's messages plus per-message
// deletion marks committed on QUIT.
type mailbox struct {
	st      *objectstore.Store
	folder  int64
	msgs    []objectstore.MessageInfo
	deleted []bool
}

func openMailbox(path string) (*mailbox, error) {
	st, err := objectstore.Open(path)
	if err != nil {
		return nil, err
	}
	// The inbox is a built-in folder provisioned at mailbox creation, addressed
	// directly by its fixed id.
	mb := &mailbox{st: st, folder: int64(mapi.PrivateFIDInbox)}
	if mb.msgs, err = st.ListMessages(mb.folder); err != nil {
		_ = st.Close()
		return nil, err
	}
	mb.deleted = make([]bool, len(mb.msgs))
	return mb, nil
}

// reset clears every deletion mark (RSET, RFC 1939 §5).
func (mb *mailbox) reset() {
	for i := range mb.deleted {
		mb.deleted[i] = false
	}
}

func (mb *mailbox) count() int {
	n := 0
	for i := range mb.msgs {
		if !mb.deleted[i] {
			n++
		}
	}
	return n
}

func (mb *mailbox) totalSize() int64 {
	var total int64
	for i, m := range mb.msgs {
		if !mb.deleted[i] {
			total += m.Size
		}
	}
	return total
}

// index parses a 1-based message number and validates it is live.
func (mb *mailbox) index(arg string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || n < 1 || n > len(mb.msgs) || mb.deleted[n-1] {
		return 0, false
	}
	return n, true
}

func (mb *mailbox) list(w *ew, arg string, uidl bool) {
	if strings.TrimSpace(arg) != "" {
		n, valid := mb.index(arg)
		if !valid {
			errLine(w, "no such message")
			return
		}
		if uidl {
			ok(w, fmt.Sprintf("%d %d", n, mb.msgs[n-1].UID))
		} else {
			ok(w, fmt.Sprintf("%d %d", n, mb.msgs[n-1].Size))
		}
		return
	}
	ok(w, fmt.Sprintf("%d messages", mb.count()))
	for i, m := range mb.msgs {
		if mb.deleted[i] {
			continue
		}
		if uidl {
			w.printf("%d %d\r\n", i+1, m.UID)
		} else {
			w.printf("%d %d\r\n", i+1, m.Size)
		}
	}
	w.str(".\r\n")
	w.flush()
}

func (mb *mailbox) retr(w *ew, arg string) {
	n, valid := mb.index(arg)
	if !valid {
		errLine(w, "no such message")
		return
	}
	raw, err := mb.st.GetMessageRaw(mb.folder, mb.msgs[n-1].UID)
	if err != nil {
		errLine(w, "[SYS/TEMP] retrieval failed")
		return
	}
	w.printf("+OK %d octets\r\n", len(raw))
	writeDotStuffed(w, raw)
	w.flush()
}

// top implements RFC 1939 TOP: it writes a message's full headers plus the first
// n lines of its body (n >= 0), dot-stuffed and terminated like RETR. It does not
// mark the message and is valid only in the TRANSACTION state.
func (mb *mailbox) top(w *ew, arg string) {
	fields := strings.Fields(arg)
	if len(fields) != 2 {
		errLine(w, "TOP requires a message number and a line count")
		return
	}
	n, valid := mb.index(fields[0])
	if !valid {
		errLine(w, "no such message")
		return
	}
	lines, err := strconv.Atoi(fields[1])
	if err != nil || lines < 0 {
		errLine(w, "invalid line count")
		return
	}
	raw, err := mb.st.GetMessageRaw(mb.folder, mb.msgs[n-1].UID)
	if err != nil {
		errLine(w, "[SYS/TEMP] retrieval failed")
		return
	}
	ok(w, "")
	writeDotStuffed(w, topBytes(raw, lines))
	w.flush()
}

// topBytes returns a message's header block (up to and including the blank line
// that separates headers from body) plus the first n lines of the body. A message
// with no header/body separator is returned whole as headers.
func topBytes(data []byte, n int) []byte {
	sep := []byte("\r\n\r\n")
	idx := bytes.Index(data, sep)
	if idx < 0 {
		return data // no body separator: all headers
	}
	head := data[:idx+len(sep)]
	body := data[idx+len(sep):]
	out := make([]byte, 0, len(head)+len(body))
	out = append(out, head...)
	count, start := 0, 0
	for i := 0; i < len(body) && count < n; i++ {
		if body[i] == '\n' {
			out = append(out, body[start:i+1]...)
			start = i + 1
			count++
		}
	}
	// A trailing partial line (no final newline) counts toward the line budget.
	if count < n && start < len(body) {
		out = append(out, body[start:]...)
	}
	return out
}

func (mb *mailbox) dele(w *ew, arg string) {
	n, valid := mb.index(arg)
	if !valid {
		errLine(w, "no such message")
		return
	}
	mb.deleted[n-1] = true
	ok(w, fmt.Sprintf("message %d deleted", n))
}

// commit applies the session's deletions to the store on QUIT (the POP3 UPDATE
// state). Each deletion soft-deletes into the Recoverable Items dumpster rather
// than purging, so a POP3-deleted message stays recoverable until retention.
// It returns the UIDs it could not delete. QUIT answers +OK regardless, because
// the deletions are already durable from the client's point of view and there is
// no POP3 way to report a partial UPDATE, so the caller records them instead: a
// message the client believes it deleted and the store still holds is otherwise
// invisible.
func (mb *mailbox) commit() []uint32 {
	var failed []uint32
	for i, del := range mb.deleted {
		if !del {
			continue
		}
		if err := mb.st.SoftDeleteMessage(mb.folder, mb.msgs[i].UID); err != nil {
			failed = append(failed, mb.msgs[i].UID)
		}
	}
	return failed
}

// writeDotStuffed writes a message body byte-stuffed (lines starting with '.'
// get an extra '.'), terminated by a CRLF and a lone "." line.
func writeDotStuffed(w *ew, data []byte) {
	atLineStart := true
	for _, b := range data {
		if atLineStart && b == '.' {
			w.wbyte('.')
		}
		w.wbyte(b)
		atLineStart = b == '\n'
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		w.str("\r\n")
	}
	w.str(".\r\n")
}

// finishAuth validates credentials, enforces the POP3/IMAP privilege gate, and on
// success opens the maildrop. It writes the POP3 reply (+OK on success, -ERR with
// the right RFC 3206 response code on failure) and returns the opened mailbox for
// the caller to install. It is the single chokepoint for USER/PASS and every SASL
// mechanism, so the privilege gate can never be bypassed.
func (s *Server) finishAuth(w *ew, conn net.Conn, user, pass string) (*mailbox, bool) {
	emit := func(level logging.Level, name string, f logging.Fields) {
		s.Logger.Emit(logging.Event{Level: level, Subsystem: logging.POP3, Name: name, User: user, RemoteAddr: conn.RemoteAddr().String(), Fields: f})
	}
	// Throttle online guessing: a client that has piled up failed logins, or an
	// account that has, is refused before the password is checked, until the
	// lockout elapses. Both axes count, so neither one host guessing at many
	// accounts nor many hosts guessing at one account slips through.
	addr := conn.RemoteAddr().String()
	if s.Limiter != nil && !s.Limiter.Allowed(addr, user) {
		emit(logging.LevelWarn, "auth.throttled", nil)
		errLine(w, "[AUTH] too many failed attempts, try again later")
		return nil, false
	}
	path, authed := directory.AuthenticateClient(s.Auth, user, pass)
	if user == "" || !authed {
		if s.Limiter != nil {
			s.Limiter.Fail(addr, user)
		}
		emit(logging.LevelWarn, "auth.fail", nil)
		errLine(w, "[AUTH] authentication failed")
		return nil, false
	}
	if s.Limiter != nil {
		s.Limiter.Succeed(addr, user)
	}
	if privs, _ := s.Auth.Privileges(user); !privs.POP3IMAP {
		emit(logging.LevelWarn, "auth.denied", logging.Fields{"service": "pop3imap"})
		errLine(w, "[AUTH] POP3/IMAP access is disabled for this account")
		return nil, false
	}
	m, err := openMailbox(path)
	if err != nil {
		s.Logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.POP3, Name: "auth.fail", User: user, RemoteAddr: conn.RemoteAddr().String(), Err: err.Error()})
		errLine(w, "[SYS/TEMP] mailbox unavailable")
		return nil, false
	}
	emit(logging.LevelInfo, "auth.ok", nil)
	ok(w, fmt.Sprintf("%d messages", m.count()))
	return m, true
}

// authSASL runs the RFC 5034 AUTH exchange. With no mechanism it lists the
// supported ones; PLAIN and LOGIN are password mechanisms funnelled through
// finishAuth (CRAM/DIGEST-MD5 are impossible against crypt_sha512 storage). It
// returns the authenticated user and opened mailbox on success.
func (s *Server) authSASL(w *ew, tp *textproto.Reader, conn net.Conn, arg string) (string, *mailbox, bool) {
	mech, initial, _ := strings.Cut(strings.TrimSpace(arg), " ")
	switch strings.ToUpper(mech) {
	case "":
		w.str("+OK\r\n")
		w.str("PLAIN\r\n")
		w.str("LOGIN\r\n")
		w.str(".\r\n")
		w.flush()
		return "", nil, false
	case "PLAIN":
		return s.authPlain(w, tp, conn, initial)
	case "LOGIN":
		return s.authLogin(w, tp, conn, initial)
	default:
		errLine(w, "[AUTH] unsupported authentication mechanism")
		return "", nil, false
	}
}

// authPlain handles AUTH PLAIN (RFC 4616): a single base64 token decoding to
// authzid NUL authcid NUL passwd, inline or after a continuation.
func (s *Server) authPlain(w *ew, tp *textproto.Reader, conn net.Conn, initial string) (string, *mailbox, bool) {
	resp, cont := saslResponse(w, tp, s.commandLineLimit(), initial, "")
	if !cont {
		errLine(w, "[AUTH] authentication cancelled")
		return "", nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(resp)
	if err != nil {
		errLine(w, "[AUTH] invalid base64")
		return "", nil, false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 {
		errLine(w, "[AUTH] malformed PLAIN response")
		return "", nil, false
	}
	m, okAuth := s.finishAuth(w, conn, parts[1], parts[2])
	return parts[1], m, okAuth
}

// authLogin handles AUTH LOGIN: the server prompts (base64) for the username then
// the password; the username may arrive inline with the AUTH command.
func (s *Server) authLogin(w *ew, tp *textproto.Reader, conn net.Conn, initial string) (string, *mailbox, bool) {
	u, cont := saslResponse(w, tp, s.commandLineLimit(), initial, "VXNlcm5hbWU6") // base64("Username:")
	if !cont {
		errLine(w, "[AUTH] authentication cancelled")
		return "", nil, false
	}
	userBytes, err := base64.StdEncoding.DecodeString(u)
	if err != nil {
		errLine(w, "[AUTH] invalid base64")
		return "", nil, false
	}
	p, cont := saslResponse(w, tp, s.commandLineLimit(), "", "UGFzc3dvcmQ6") // base64("Password:")
	if !cont {
		errLine(w, "[AUTH] authentication cancelled")
		return "", nil, false
	}
	passBytes, err := base64.StdEncoding.DecodeString(p)
	if err != nil {
		errLine(w, "[AUTH] invalid base64")
		return "", nil, false
	}
	user := string(userBytes)
	m, okAuth := s.finishAuth(w, conn, user, string(passBytes))
	return user, m, okAuth
}

// saslResponse returns the client's SASL token: the inline value when present (a
// lone "=" is a zero-length initial response, RFC 5034), otherwise a "+ <challenge>"
// continuation is sent and the base64 line read. A lone "*" aborts the exchange.
func saslResponse(w *ew, tp *textproto.Reader, max int, inline, challenge string) (string, bool) {
	resp := inline
	switch resp {
	case "=":
		return "", true
	case "":
		w.printf("+ %s\r\n", challenge)
		w.flush()
		// The same cap as a command line: this reader is reached before the client
		// has authenticated.
		line, err := netline.ReadLine(tp.R, max)
		if err != nil {
			return "", false
		}
		resp = line
	}
	if resp == "*" {
		return "", false
	}
	return resp, true
}

// writeCapa emits the RFC 2449 CAPA list. It advertises every optional command and
// extension hermEX supports: TOP (RFC 1939), UIDL, RESP-CODES + LOGIN-DELAY + EXPIRE
// + PIPELINING + IMPLEMENTATION (RFC 2449), UTF8 + LANG (RFC 6856), and STLS (RFC
// 2595) only when a TLS config is present and the link is not already encrypted.
func (s *Server) writeCapa(w *ew, isTLS bool) {
	w.str("+OK Capability list follows\r\n")
	w.str("TOP\r\n")
	w.str("USER\r\n")
	w.str("UIDL\r\n")
	w.str("SASL PLAIN LOGIN\r\n")
	w.str("PIPELINING\r\n")
	w.str("RESP-CODES\r\n")
	w.str("LOGIN-DELAY 0\r\n")
	w.str("EXPIRE NEVER\r\n")
	w.str("UTF8\r\n")
	w.str("LANG\r\n")
	if s.TLSConfig != nil && !isTLS {
		w.str("STLS\r\n")
	}
	w.str("IMPLEMENTATION hermEX\r\n")
	w.str(".\r\n")
	w.flush()
}

// writeLang answers the RFC 6856 LANG command. With no argument it lists the
// supported languages; with a basic language range matching "en" (or "*" for the
// default) it selects English. hermEX only emits English response text, so any
// other range is refused.
func writeLang(w *ew, arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		w.str("+OK Language listing follows:\r\n")
		w.str("en English\r\n")
		w.str(".\r\n")
		w.flush()
		return
	}
	low := strings.ToLower(arg)
	if arg == "*" || low == "en" || strings.HasPrefix(low, "en-") {
		ok(w, "en Responses will be in English")
		return
	}
	errLine(w, "invalid language, only en is available")
}

func ok(w *ew, msg string) {
	if msg == "" {
		w.str("+OK\r\n")
	} else {
		w.printf("+OK %s\r\n", msg)
	}
	w.flush()
}

func errLine(w *ew, msg string) {
	w.printf("-ERR %s\r\n", msg)
	w.flush()
}

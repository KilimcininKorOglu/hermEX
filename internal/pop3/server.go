// Package pop3 implements an RFC 1939 POP3 retrieval server backed by the mailbox
// store, with the CAPA extension mechanism (RFC 2449), STLS (RFC 2595), and UTF8 +
// LANG (RFC 6856). It authenticates with USER/PASS through a directory.Authenticator
// and serves a login-time snapshot of the INBOX.
package pop3

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/textproto"
	"strconv"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// Server accepts POP3 connections and serves mailboxes resolved via Auth.
type Server struct {
	Auth      directory.Authenticator
	Hostname  string
	TLSConfig *tls.Config     // when non-nil, advertise (CAPA) and accept STLS
	Logger    *logging.Logger // central activity log; nil disables logging

	conns lifecycle.ConnGroup
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

func (s *Server) handle(conn net.Conn) {
	defer func() { conn.Close() }() // closes the upgraded conn after an STLS swap
	w := bufio.NewWriter(conn)
	tp := textproto.NewReader(bufio.NewReader(conn))
	_, isTLS := conn.(*tls.Conn)

	var mb *mailbox
	defer func() {
		if mb != nil {
			mb.st.Close()
		}
	}()

	ok(w, "hermEX POP3 ready")

	var user string
	// event logs through the server's logger, reading the live user and connection
	// (both change mid-session: user on login, conn on an STLS upgrade). A nil
	// logger is a no-op.
	event := func(level logging.Level, name string, f logging.Fields) {
		s.Logger.Emit(logging.Event{
			Level:      level,
			Subsystem:  logging.POP3,
			Name:       name,
			User:       user,
			RemoteAddr: conn.RemoteAddr().String(),
			Fields:     f,
		})
	}
	event(logging.LevelInfo, "conn.accept", logging.Fields{"tls": isTLS})

	for {
		line, err := tp.ReadLine()
		if err != nil {
			return // client gone; per RFC no deletions are committed
		}
		cmd, arg, _ := strings.Cut(line, " ")
		cmd = strings.ToUpper(cmd)

		// Per-command audit at debug level — the verb only, never the argument
		// (PASS's argument is the password).
		event(logging.LevelDebug, "command", logging.Fields{"cmd": cmd})

		// CAPA (RFC 2449) and LANG (RFC 6856) are valid in both the AUTHORIZATION
		// and TRANSACTION states, so handle them before the state split.
		switch cmd {
		case "CAPA":
			s.writeCapa(w, isTLS)
			continue
		case "LANG":
			writeLang(w, arg)
			continue
		}

		if mb == nil { // AUTHORIZATION state
			switch cmd {
			case "USER":
				user = arg
				ok(w, "")
			case "PASS":
				path, authed := s.Auth.Authenticate(user, arg)
				if user == "" || !authed {
					event(logging.LevelWarn, "auth.fail", nil) // attempted login still in user
					user = ""
					errLine(w, "[AUTH] authentication failed")
					continue
				}
				if privs, _ := s.Auth.Privileges(user); !privs.POP3IMAP {
					event(logging.LevelWarn, "auth.denied", logging.Fields{"service": "pop3imap"})
					user = ""
					errLine(w, "[AUTH] POP3/IMAP access is disabled for this account")
					continue
				}
				m, err := openMailbox(path)
				if err != nil {
					s.Logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.POP3, Name: "auth.fail", User: user, RemoteAddr: conn.RemoteAddr().String(), Err: err.Error()})
					errLine(w, "[SYS/TEMP] mailbox unavailable")
					continue
				}
				mb = m
				event(logging.LevelInfo, "auth.ok", nil)
				ok(w, fmt.Sprintf("%d messages", mb.count()))
			case "UTF8":
				// RFC 6856: enter UTF-8 mode (valid only in AUTHORIZATION). hermEX
				// serves the stored message bytes verbatim and never downgrades, so
				// this is an acknowledgment with no behavior change.
				ok(w, "UTF-8 mode enabled")
				event(logging.LevelInfo, "utf8", nil)
			case "STLS":
				if s.TLSConfig == nil || isTLS {
					errLine(w, "STLS not available")
					continue
				}
				if tp.R.Buffered() > 0 {
					event(logging.LevelWarn, "stls.injection", nil)
					return // pipelined plaintext behind STLS; abort the connection
				}
				ok(w, "begin TLS negotiation")
				tc := tls.Server(conn, s.TLSConfig)
				if err := tc.Handshake(); err != nil {
					return // handshake failed; deferred close fires
				}
				conn = tc
				w = bufio.NewWriter(tc)
				tp = textproto.NewReader(bufio.NewReader(tc))
				isTLS = true
				user = "" // discard any USER given before TLS
				event(logging.LevelInfo, "stls", nil)
			case "QUIT":
				ok(w, "bye")
				return
			default:
				errLine(w, "command not allowed before authentication")
			}
			continue
		}

		// TRANSACTION state
		switch cmd {
		case "STAT":
			ok(w, fmt.Sprintf("%d %d", mb.count(), mb.totalSize()))
		case "LIST":
			mb.list(w, arg, false)
		case "UIDL":
			mb.list(w, arg, true)
		case "TOP":
			mb.top(w, arg)
		case "RETR":
			mb.retr(w, arg)
		case "DELE":
			mb.dele(w, arg)
		case "RSET":
			for i := range mb.deleted {
				mb.deleted[i] = false
			}
			ok(w, "")
		case "NOOP":
			ok(w, "")
		case "QUIT":
			mb.commit()
			ok(w, "bye")
			return
		default:
			errLine(w, "unknown command")
		}
	}
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
		st.Close()
		return nil, err
	}
	mb.deleted = make([]bool, len(mb.msgs))
	return mb, nil
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

func (mb *mailbox) list(w *bufio.Writer, arg string, uidl bool) {
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
			fmt.Fprintf(w, "%d %d\r\n", i+1, m.UID)
		} else {
			fmt.Fprintf(w, "%d %d\r\n", i+1, m.Size)
		}
	}
	w.WriteString(".\r\n")
	w.Flush()
}

func (mb *mailbox) retr(w *bufio.Writer, arg string) {
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
	fmt.Fprintf(w, "+OK %d octets\r\n", len(raw))
	writeDotStuffed(w, raw)
	w.Flush()
}

// top implements RFC 1939 TOP: it writes a message's full headers plus the first
// n lines of its body (n >= 0), dot-stuffed and terminated like RETR. It does not
// mark the message and is valid only in the TRANSACTION state.
func (mb *mailbox) top(w *bufio.Writer, arg string) {
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
	w.Flush()
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

func (mb *mailbox) dele(w *bufio.Writer, arg string) {
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
func (mb *mailbox) commit() {
	for i, del := range mb.deleted {
		if del {
			mb.st.SoftDeleteMessage(mb.folder, mb.msgs[i].UID)
		}
	}
}

// writeDotStuffed writes a message body byte-stuffed (lines starting with '.'
// get an extra '.'), terminated by a CRLF and a lone "." line.
func writeDotStuffed(w *bufio.Writer, data []byte) {
	atLineStart := true
	for _, b := range data {
		if atLineStart && b == '.' {
			w.WriteByte('.')
		}
		w.WriteByte(b)
		atLineStart = b == '\n'
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		w.WriteString("\r\n")
	}
	w.WriteString(".\r\n")
}

// writeCapa emits the RFC 2449 CAPA list. It advertises every optional command and
// extension hermEX supports: TOP (RFC 1939), UIDL, RESP-CODES + LOGIN-DELAY + EXPIRE
// + PIPELINING + IMPLEMENTATION (RFC 2449), UTF8 + LANG (RFC 6856), and STLS (RFC
// 2595) only when a TLS config is present and the link is not already encrypted.
func (s *Server) writeCapa(w *bufio.Writer, isTLS bool) {
	w.WriteString("+OK Capability list follows\r\n")
	w.WriteString("TOP\r\n")
	w.WriteString("USER\r\n")
	w.WriteString("UIDL\r\n")
	w.WriteString("PIPELINING\r\n")
	w.WriteString("RESP-CODES\r\n")
	w.WriteString("LOGIN-DELAY 0\r\n")
	w.WriteString("EXPIRE NEVER\r\n")
	w.WriteString("UTF8\r\n")
	w.WriteString("LANG\r\n")
	if s.TLSConfig != nil && !isTLS {
		w.WriteString("STLS\r\n")
	}
	w.WriteString("IMPLEMENTATION hermEX\r\n")
	w.WriteString(".\r\n")
	w.Flush()
}

// writeLang answers the RFC 6856 LANG command. With no argument it lists the
// supported languages; with a basic language range matching "en" (or "*" for the
// default) it selects English. hermEX only emits English response text, so any
// other range is refused.
func writeLang(w *bufio.Writer, arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		w.WriteString("+OK Language listing follows:\r\n")
		w.WriteString("en English\r\n")
		w.WriteString(".\r\n")
		w.Flush()
		return
	}
	low := strings.ToLower(arg)
	if arg == "*" || low == "en" || strings.HasPrefix(low, "en-") {
		ok(w, "en Responses will be in English")
		return
	}
	errLine(w, "invalid language, only en is available")
}

func ok(w *bufio.Writer, msg string) {
	if msg == "" {
		w.WriteString("+OK\r\n")
	} else {
		fmt.Fprintf(w, "+OK %s\r\n", msg)
	}
	w.Flush()
}

func errLine(w *bufio.Writer, msg string) {
	fmt.Fprintf(w, "-ERR %s\r\n", msg)
	w.Flush()
}

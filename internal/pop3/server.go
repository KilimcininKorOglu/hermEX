// Package pop3 implements a minimal RFC 1939 POP3 retrieval server backed by
// the mailbox store. It authenticates with USER/PASS through a
// directory.Authenticator and serves a login-time snapshot of the INBOX.
package pop3

import (
	"bufio"
	"fmt"
	"net"
	"net/textproto"
	"strconv"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/store"
)

const inboxName = "INBOX"

// Server accepts POP3 connections and serves mailboxes resolved via Auth.
type Server struct {
	Auth     directory.Authenticator
	Hostname string
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

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	tp := textproto.NewReader(bufio.NewReader(conn))

	var mb *mailbox
	defer func() {
		if mb != nil {
			mb.st.Close()
		}
	}()

	ok(w, "hermEX POP3 ready")

	var user string
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return // client gone; per RFC no deletions are committed
		}
		cmd, arg, _ := strings.Cut(line, " ")
		cmd = strings.ToUpper(cmd)

		if mb == nil { // AUTHORIZATION state
			switch cmd {
			case "USER":
				user = arg
				ok(w, "")
			case "PASS":
				path, authed := s.Auth.Authenticate(user, arg)
				if user == "" || !authed {
					user = ""
					errLine(w, "authentication failed")
					continue
				}
				m, err := openMailbox(path)
				if err != nil {
					errLine(w, "mailbox unavailable")
					continue
				}
				mb = m
				ok(w, fmt.Sprintf("%d messages", mb.count()))
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
	st      *store.Store
	folder  int64
	msgs    []store.MessageInfo
	deleted []bool
}

func openMailbox(path string) (*mailbox, error) {
	st, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	mb := &mailbox{st: st}
	inbox, found, err := st.FolderByName(nil, inboxName)
	if err != nil {
		st.Close()
		return nil, err
	}
	if found {
		mb.folder = inbox
		if mb.msgs, err = st.ListMessages(inbox); err != nil {
			st.Close()
			return nil, err
		}
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
		errLine(w, "retrieval failed")
		return
	}
	fmt.Fprintf(w, "+OK %d octets\r\n", len(raw))
	writeDotStuffed(w, raw)
	w.Flush()
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

// commit applies the session's deletions to the store (the POP3 UPDATE state).
func (mb *mailbox) commit() {
	for i, del := range mb.deleted {
		if del {
			mb.st.DeleteMessage(mb.folder, mb.msgs[i].UID)
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

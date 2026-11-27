// Package fetchmail polls remote POP3/IMAP accounts and delivers their new mail into a
// local mailbox. It is an all-Go implementation of the classic fetchmail role: the
// clients here speak the wire protocols directly and the worker drives them from the
// stored per-mailbox configurations.
package fetchmail

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// opTimeout bounds each protocol operation's reads once connected, so a remote that accepts
// the connection but then stalls cannot hang the worker forever. It is refreshed before
// every command, so the deadline advances with progress and a long-but-moving transfer is
// not cut off. It is a var so tests can shorten it.
var opTimeout = 2 * time.Minute

// setDeadline arms the post-connect I/O deadline. Called before each read phase; because the
// deadline is absolute and re-armed each time, a stalled remote read fails after opTimeout
// while a transfer that keeps making progress is never interrupted. A connection
// that refuses the deadline would let a stalled remote hang the worker, so the
// failure ends the operation.
func setDeadline(conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(opTimeout)); err != nil {
		return fmt.Errorf("fetchmail: set the I/O deadline: %w", err)
	}
	return nil
}

// pop3Conn is a minimal POP3 client: enough of RFC 1939 to list, identify, download, and
// optionally delete messages.
type pop3Conn struct {
	conn   net.Conn
	tp     *textproto.Conn
	budget *responseBudget
}

// budgetedConn reads through the response budget and writes and closes the
// connection directly.
type budgetedConn struct {
	io.Reader
	net.Conn
}

func (b budgetedConn) Read(p []byte) (int, error) { return b.Reader.Read(p) }

// dialPOP3 connects to a POP3 source. With ssl it uses implicit TLS (POP3S); otherwise a
// plain connection. verify=false accepts a self-signed server certificate. A zero port
// selects the protocol default (995 for SSL, 110 otherwise).
func dialPOP3(server string, port int, ssl, verify bool) (*pop3Conn, error) {
	if port == 0 {
		if ssl {
			port = 995
		} else {
			port = 110
		}
	}
	addr := net.JoinHostPort(server, strconv.Itoa(port))
	var conn net.Conn
	var err error
	if ssl {
		conn, err = tlsDialSource(addr, server, verify)
	} else {
		conn, err = dialSource(addr)
	}
	if err != nil {
		return nil, err
	}
	budget := newResponseBudget(conn)
	c := &pop3Conn{conn: conn, tp: textproto.NewConn(budgetedConn{Reader: budget, Conn: conn}), budget: budget}
	if err := setDeadline(conn); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	if _, err := c.readOK(); err != nil { // server greeting
		return nil, errors.Join(err, conn.Close())
	}
	return c, nil
}

// readOK reads one status line, returning the text after "+OK" or an error for "-ERR".
func (c *pop3Conn) readOK() (string, error) {
	line, err := c.tp.ReadLine()
	if err != nil {
		return "", err
	}
	if c.budget.spent() {
		return "", ErrResponseTooLarge
	}
	if rest, ok := strings.CutPrefix(line, "+OK"); ok {
		return strings.TrimSpace(rest), nil
	}
	return "", fmt.Errorf("pop3: %s", line)
}

// cmd sends a command and reads its single-line status reply.
func (c *pop3Conn) cmd(format string, args ...any) (string, error) {
	if err := setDeadline(c.conn); err != nil {
		return "", err
	}
	c.budget.renew()
	if err := c.tp.PrintfLine(format, args...); err != nil {
		return "", err
	}
	return c.readOK()
}

// startTLS switches a plaintext session to TLS with STLS (RFC 2595 4) before any
// credential is sent. A source that refuses the command gets no password, and a
// byte sent after the +OK, before the handshake, ends the session.
func (c *pop3Conn) startTLS(serverName string, verify bool) error {
	if _, err := c.cmd("STLS"); err != nil {
		return errors.Join(ErrNoStartTLS, err)
	}
	if c.tp.R.Buffered() > 0 {
		return errors.New("fetchmail: pop3 source sent data behind STLS")
	}
	tc, err := upgradeTLS(c.conn, serverName, verify)
	if err != nil {
		return err
	}
	c.conn = tc
	c.budget = newResponseBudget(tc)
	c.tp = textproto.NewConn(budgetedConn{Reader: c.budget, Conn: tc})
	return nil
}

// auth performs USER/PASS authentication.
func (c *pop3Conn) auth(user, pass string) error {
	if err := lineSafe(user, pass); err != nil {
		return err
	}
	if _, err := c.cmd("USER %s", user); err != nil {
		return err
	}
	if _, err := c.cmd("PASS %s", pass); err != nil {
		return err
	}
	return nil
}

// uidl returns message number → persistent unique-id for every message in the maildrop,
// the basis for not re-delivering a kept message on a later poll.
func (c *pop3Conn) uidl() (map[int]string, error) {
	if _, err := c.cmd("UIDL"); err != nil {
		return nil, err
	}
	if err := setDeadline(c.conn); err != nil {
		return nil, err
	}
	lines, err := c.tp.ReadDotLines()
	if err != nil {
		return nil, err
	}
	if c.budget.spent() {
		return nil, ErrResponseTooLarge
	}
	out := make(map[int]string, len(lines))
	for _, ln := range lines {
		if f := strings.Fields(ln); len(f) >= 2 {
			if n, err := strconv.Atoi(f[0]); err == nil {
				out[n] = f[1]
			}
		}
	}
	return out, nil
}

// retr downloads message n as a raw RFC 822 message with CRLF line endings,
// reading at most maxMessage bytes. The dot-stream is drained either way, so an
// over-cap message costs the cap plus discarded bytes and leaves the session
// usable for the next message rather than desynced.
func (c *pop3Conn) retr(n int) ([]byte, error) {
	if _, err := c.cmd("RETR %d", n); err != nil {
		return nil, err
	}
	if err := setDeadline(c.conn); err != nil {
		return nil, err
	}
	dot := c.tp.DotReader()
	limit := maxMessage()
	body, err := io.ReadAll(io.LimitReader(dot, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		// A drain that fails leaves the session mid-message, so the next command
		// would read this message's bytes as its reply; the caller must stop.
		if _, err := io.Copy(io.Discard, dot); err != nil {
			return nil, errors.Join(ErrMessageTooLarge, fmt.Errorf("fetchmail: drain the over-cap message: %w", err))
		}
		return nil, ErrMessageTooLarge
	}
	// DotReader hands back LF endings; the wire form is CRLF, which is what
	// ReadDotLines-then-join produced before.
	return bytes.ReplaceAll(body, []byte("\n"), []byte("\r\n")), nil
}

// dele marks message n for deletion (applied by the server on QUIT).
func (c *pop3Conn) dele(n int) error {
	_, err := c.cmd("DELE %d", n)
	return err
}

// quit ends the session, committing any pending deletions, and closes the connection.
func (c *pop3Conn) quit() error {
	_, err := c.cmd("QUIT")
	return errors.Join(err, c.tp.Close())
}

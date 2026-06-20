// Package fetchmail polls remote POP3/IMAP accounts and delivers their new mail into a
// local mailbox. It is an all-Go implementation of the classic fetchmail role: the
// clients here speak the wire protocols directly and the worker drives them from the
// stored per-mailbox configurations.
package fetchmail

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// dialTimeout bounds the TCP/TLS connect to a remote source server.
const dialTimeout = 30 * time.Second

// opTimeout bounds each protocol operation's reads once connected, so a remote that accepts
// the connection but then stalls cannot hang the worker forever. It is refreshed before
// every command, so the deadline advances with progress and a long-but-moving transfer is
// not cut off. It is a var so tests can shorten it.
var opTimeout = 2 * time.Minute

// setDeadline arms the post-connect I/O deadline. Called before each read phase; because the
// deadline is absolute and re-armed each time, a stalled remote read fails after opTimeout
// while a transfer that keeps making progress is never interrupted.
func setDeadline(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(opTimeout))
}

// pop3Conn is a minimal POP3 client: enough of RFC 1939 to list, identify, download, and
// optionally delete messages.
type pop3Conn struct {
	conn net.Conn
	tp   *textproto.Conn
}

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
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: dialTimeout}, "tcp", addr,
			&tls.Config{ServerName: server, InsecureSkipVerify: !verify}) //nolint:gosec // verify is the admin's explicit choice
	} else {
		conn, err = net.DialTimeout("tcp", addr, dialTimeout)
	}
	if err != nil {
		return nil, err
	}
	c := &pop3Conn{conn: conn, tp: textproto.NewConn(conn)}
	setDeadline(conn)
	if _, err := c.readOK(); err != nil { // server greeting
		conn.Close()
		return nil, err
	}
	return c, nil
}

// readOK reads one status line, returning the text after "+OK" or an error for "-ERR".
func (c *pop3Conn) readOK() (string, error) {
	line, err := c.tp.ReadLine()
	if err != nil {
		return "", err
	}
	if rest, ok := strings.CutPrefix(line, "+OK"); ok {
		return strings.TrimSpace(rest), nil
	}
	return "", fmt.Errorf("pop3: %s", line)
}

// cmd sends a command and reads its single-line status reply.
func (c *pop3Conn) cmd(format string, args ...any) (string, error) {
	setDeadline(c.conn)
	if err := c.tp.PrintfLine(format, args...); err != nil {
		return "", err
	}
	return c.readOK()
}

// auth performs USER/PASS authentication.
func (c *pop3Conn) auth(user, pass string) error {
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
	setDeadline(c.conn)
	lines, err := c.tp.ReadDotLines()
	if err != nil {
		return nil, err
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

// retr downloads message n as a raw RFC 822 message with CRLF line endings.
func (c *pop3Conn) retr(n int) ([]byte, error) {
	if _, err := c.cmd("RETR %d", n); err != nil {
		return nil, err
	}
	setDeadline(c.conn)
	lines, err := c.tp.ReadDotLines()
	if err != nil {
		return nil, err
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n"), nil
}

// dele marks message n for deletion (applied by the server on QUIT).
func (c *pop3Conn) dele(n int) error {
	_, err := c.cmd("DELE %d", n)
	return err
}

// quit ends the session, committing any pending deletions, and closes the connection.
func (c *pop3Conn) quit() error {
	_, err := c.cmd("QUIT")
	c.tp.Close()
	return err
}

package fetchmail

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// imapConn is a minimal IMAP client: enough of RFC 3501 to select a folder, find
// messages by UID, download a message body, and flag it seen or deleted.
type imapConn struct {
	conn net.Conn
	r    *bufio.Reader
	tag  int
}

// dialIMAP connects to an IMAP source. With ssl it uses implicit TLS (IMAPS); otherwise a
// plain connection. verify=false accepts a self-signed certificate. A zero port selects
// the protocol default (993 for SSL, 143 otherwise).
func dialIMAP(server string, port int, ssl, verify bool) (*imapConn, error) {
	if port == 0 {
		if ssl {
			port = 993
		} else {
			port = 143
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
	c := &imapConn{conn: conn, r: bufio.NewReader(conn)}
	setDeadline(conn)
	line, err := c.r.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.HasPrefix(line, "* OK") && !strings.HasPrefix(line, "* PREAUTH") {
		conn.Close()
		return nil, fmt.Errorf("imap greeting: %s", strings.TrimSpace(line))
	}
	return c, nil
}

// do sends a tagged command and reads to its completion, returning the untagged response
// lines. It is for line-based commands; fetchBody handles the literal-bearing FETCH.
func (c *imapConn) do(cmd string) ([]string, error) {
	setDeadline(c.conn)
	c.tag++
	tag := fmt.Sprintf("a%d", c.tag)
	if _, err := io.WriteString(c.conn, tag+" "+cmd+"\r\n"); err != nil {
		return nil, err
	}
	var untagged []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		t := strings.TrimRight(line, "\r\n")
		if rest, ok := strings.CutPrefix(t, tag+" "); ok {
			if strings.HasPrefix(rest, "OK") {
				return untagged, nil
			}
			return untagged, fmt.Errorf("imap: %s", rest)
		}
		untagged = append(untagged, t)
	}
}

func (c *imapConn) login(user, pass string) error {
	_, err := c.do("LOGIN " + quoteIMAP(user) + " " + quoteIMAP(pass))
	return err
}

func (c *imapConn) selectFolder(name string) error {
	if name == "" {
		name = "INBOX"
	}
	_, err := c.do("SELECT " + quoteIMAP(name))
	return err
}

// search returns the UIDs matching criteria ("ALL" or "UNSEEN").
func (c *imapConn) search(criteria string) ([]string, error) {
	lines, err := c.do("UID SEARCH " + criteria)
	if err != nil {
		return nil, err
	}
	var uids []string
	for _, ln := range lines {
		if rest, ok := strings.CutPrefix(ln, "* SEARCH"); ok {
			uids = append(uids, strings.Fields(rest)...)
		}
	}
	return uids, nil
}

// fetchBody downloads one message's full body by UID, reading the literal precisely.
func (c *imapConn) fetchBody(uid string) ([]byte, error) {
	setDeadline(c.conn)
	c.tag++
	tag := fmt.Sprintf("a%d", c.tag)
	if _, err := io.WriteString(c.conn, tag+" UID FETCH "+uid+" (BODY.PEEK[])\r\n"); err != nil {
		return nil, err
	}
	var body []byte
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		t := strings.TrimRight(line, "\r\n")
		if n, ok := literalSize(t); ok {
			setDeadline(c.conn)
			buf := make([]byte, n)
			if _, err := io.ReadFull(c.r, buf); err != nil {
				return nil, err
			}
			body = buf
			if _, err := c.r.ReadString('\n'); err != nil { // trailing ")" after the literal
				return nil, err
			}
			continue
		}
		if rest, ok := strings.CutPrefix(t, tag+" "); ok {
			if strings.HasPrefix(rest, "OK") {
				return body, nil
			}
			return nil, fmt.Errorf("imap fetch: %s", rest)
		}
	}
}

func (c *imapConn) markSeen(uid string) error {
	_, err := c.do("UID STORE " + uid + " +FLAGS (\\Seen)")
	return err
}

// deleteMessage flags a message deleted and expunges it from the folder.
func (c *imapConn) deleteMessage(uid string) error {
	if _, err := c.do("UID STORE " + uid + " +FLAGS (\\Deleted)"); err != nil {
		return err
	}
	_, err := c.do("EXPUNGE")
	return err
}

func (c *imapConn) logout() error {
	_, err := c.do("LOGOUT")
	c.conn.Close()
	return err
}

// literalSize reports the byte count of a trailing IMAP literal marker "{N}".
func literalSize(line string) (int, bool) {
	i := strings.LastIndex(line, "{")
	if i < 0 || !strings.HasSuffix(line, "}") {
		return 0, false
	}
	n, err := strconv.Atoi(line[i+1 : len(line)-1])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// quoteIMAP wraps a string as an IMAP quoted string, escaping the special characters.
func quoteIMAP(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

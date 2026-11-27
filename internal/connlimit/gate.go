package connlimit

import (
	"errors"
	"fmt"
	"net"
	"time"

	"hermex/internal/clientnet"
)

// refusalWriteTimeout bounds how long the refusal write may take. A client that
// never reads its refusal must not delay the daemon's drain.
const refusalWriteTimeout = 5 * time.Second

// ClientKey is the key a connection is counted under: the client its remote
// address names (clientnet.Key), so one client's connections count together
// whatever source ports, or IPv6 addresses inside its /64, they use. It is empty
// when the address cannot be read, and an empty key counts against the total only.
func ClientKey(nc net.Conn) string {
	if nc == nil || nc.RemoteAddr() == nil {
		return ""
	}
	return clientnet.Key(nc.RemoteAddr().String())
}

// Gate builds the admission gate and refusal writer a connection-oriented server
// installs on its accept loop. line is the protocol's own refusal (an IMAP
// untagged BYE, a POP3 -ERR, an SMTP 421), written before the connection closes so
// the client is told why rather than seeing a bare disconnect. onRefused reports
// the client address and the cap that refused it, so each protocol logs the event
// under its own subsystem; it may be nil. onRefusalFailed reports a refusal that
// did not reach the client or a connection that did not close; it may be nil.
func Gate(l *Limiter, line string, onRefused func(remote string, why Reason),
	onRefusalFailed func(remote string, err error)) (
	gate func(net.Conn) (func(), bool), refuse func(net.Conn)) {
	gate = func(nc net.Conn) (func(), bool) {
		release, ok, why := l.Acquire(ClientKey(nc))
		if !ok && onRefused != nil {
			onRefused(remoteAddr(nc), why)
		}
		return release, ok
	}
	refuse = func(nc net.Conn) {
		if err := deliverRefusal(nc, line); err != nil && onRefusalFailed != nil {
			onRefusalFailed(remoteAddr(nc), err)
		}
	}
	return gate, refuse
}

// deliverRefusal writes line within refusalWriteTimeout and closes nc. Without
// the deadline the write is not attempted, since a client that never reads would
// hold it; the connection is closed either way.
func deliverRefusal(nc net.Conn, line string) (err error) {
	defer func() {
		if cerr := nc.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close: %w", cerr))
		}
	}()
	if err := nc.SetWriteDeadline(time.Now().Add(refusalWriteTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if _, err := nc.Write([]byte(line)); err != nil {
		return fmt.Errorf("write refusal: %w", err)
	}
	return nil
}

// remoteAddr returns a connection's remote address for the log line, or "".
func remoteAddr(nc net.Conn) string {
	if nc == nil || nc.RemoteAddr() == nil {
		return ""
	}
	return nc.RemoteAddr().String()
}

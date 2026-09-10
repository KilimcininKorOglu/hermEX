package connlimit

import (
	"net"
	"time"
)

// refusalWriteTimeout bounds how long the refusal write may take. A client that
// never reads its refusal must not delay the daemon's drain.
const refusalWriteTimeout = 5 * time.Second

// ClientKey is the key a connection is counted under: the host half of its remote
// address, so one client's connections count together whatever source ports they
// use. It is empty when the address cannot be read, and an empty key counts
// against the total only.
func ClientKey(nc net.Conn) string {
	if nc == nil || nc.RemoteAddr() == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(nc.RemoteAddr().String())
	if err != nil {
		return ""
	}
	return host
}

// Gate builds the admission gate and refusal writer a connection-oriented server
// installs on its accept loop. line is the protocol's own refusal (an IMAP
// untagged BYE, a POP3 -ERR, an SMTP 421), written before the connection closes so
// the client is told why rather than seeing a bare disconnect. onRefused reports
// the client address and the cap that refused it, so each protocol logs the event
// under its own subsystem; it may be nil.
func Gate(l *Limiter, line string, onRefused func(remote string, why Reason)) (
	gate func(net.Conn) (func(), bool), refuse func(net.Conn)) {
	gate = func(nc net.Conn) (func(), bool) {
		release, ok, why := l.Acquire(ClientKey(nc))
		if !ok && onRefused != nil {
			onRefused(remoteAddr(nc), why)
		}
		return release, ok
	}
	refuse = func(nc net.Conn) {
		defer func() { _ = nc.Close() }()
		_ = nc.SetWriteDeadline(time.Now().Add(refusalWriteTimeout))
		_, _ = nc.Write([]byte(line))
	}
	return gate, refuse
}

// remoteAddr returns a connection's remote address for the log line, or "".
func remoteAddr(nc net.Conn) string {
	if nc == nil || nc.RemoteAddr() == nil {
		return ""
	}
	return nc.RemoteAddr().String()
}

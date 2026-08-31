package fetchmail

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync/atomic"

	"hermex/internal/ssrfguard"
)

// defaultMaxMessage bounds one fetched message when no operator limit is set. A
// remote source decides how many bytes it sends, and both mini-clients used to
// buffer whatever arrived, so this is the ceiling that keeps one message from
// costing the worker its memory. It is generous enough that no legitimate
// message reaches it.
const defaultMaxMessage = 64 << 20

// ErrMessageTooLarge reports a source message over the cap. The message is left
// on the source server: refusing it is better than dropping it silently, and an
// operator who wants it can raise the limit.
var ErrMessageTooLarge = errors.New("fetchmail: message exceeds the size limit")

// maxMessageBytes is the operator's per-message cap; 0 selects defaultMaxMessage.
// The worker is a per-process singleton, so a package-level value is the right
// scope (mirrors the other daemons' size-limit setters).
var maxMessageBytes atomic.Int64

// SetMaxMessage sets the largest message the fetch clients will read, in bytes.
// 0 restores the built-in default, and a negative value is treated as 0: "no
// operator limit" must still leave a ceiling, because the point of the cap is to
// bound an allocation a remote server controls.
func SetMaxMessage(n int64) {
	if n < 0 {
		n = 0
	}
	maxMessageBytes.Store(n)
}

// maxMessage returns the cap in force.
func maxMessage() int64 {
	if n := maxMessageBytes.Load(); n > 0 {
		return n
	}
	return defaultMaxMessage
}

// allowInternalSources decides whether a configured source may resolve to a
// loopback, link-local or private address. It defaults to false: a fetchmail entry
// is created by a domain-scoped admin, so without the block that role could point
// a source at this deployment's own internal services (the admin API, the
// databases, the notify relay) or fingerprint the internal network from the
// per-attempt failure modes. An install that legitimately fetches from an
// on-premises server turns it on, which is a system-administrator decision.
var allowInternalSources atomic.Bool

// SetAllowInternalSources applies the operator's source policy; the worker's poll
// calls it, so a change takes effect without a restart.
func SetAllowInternalSources(on bool) { allowInternalSources.Store(on) }

// dialSource connects to a source server through the address-range guard, which
// resolves the host and dials the resolved address, so the check cannot be
// side-stepped by a name that resolves differently the second time.
func dialSource(addr string) (net.Conn, error) {
	return ssrfguard.GuardedDial(allowInternalSources.Load())(context.Background(), "tcp", addr)
}

// tlsDialSource is dialSource plus the TLS handshake, with the certificate checked
// against the configured server name rather than the resolved address.
func tlsDialSource(addr, serverName string, verify bool) (net.Conn, error) {
	raw, err := dialSource(addr)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, &tls.Config{ServerName: serverName, InsecureSkipVerify: !verify}) // #nosec G402 -- verify is the admin's explicit choice, skipped only when the operator disabled it
	if err := conn.HandshakeContext(context.Background()); err != nil {
		return nil, errors.Join(err, raw.Close())
	}
	return conn, nil
}

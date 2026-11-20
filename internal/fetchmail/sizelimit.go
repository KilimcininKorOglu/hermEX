package fetchmail

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
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

// ErrResponseTooLarge reports a server response, other than a message body, that
// ran past the per-command budget: an endless status line or listing.
var ErrResponseTooLarge = errors.New("fetchmail: server response exceeds the size limit")

// responseSlack is what one command may read beyond the message cap: room for the
// status lines and literal framing around a message of the largest allowed size.
const responseSlack = 64 << 10

// responseBudget bounds every byte a client reads from the source within one
// command. The source decides how much it sends, and a status line, a UIDL or
// SEARCH listing is buffered whole, so without a budget a server that never ends
// a line costs the worker its memory. The budget is renewed before each command,
// so a long session of ordinary replies is not cut off.
type responseBudget struct {
	r    io.Reader
	left int64
}

// renew restores the budget to the message cap plus the framing slack.
func (b *responseBudget) renew() { b.left = maxMessage() + responseSlack }

func (b *responseBudget) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, ErrResponseTooLarge
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	return n, err
}

// spent reports whether the budget ran out. A line reader hands back the bytes it
// had buffered as if they were a whole line when the next read fails, so a caller
// must check this rather than trust that a returned line was complete.
func (b *responseBudget) spent() bool { return b.left <= 0 }

// newResponseBudget wraps r with a budget already renewed for the greeting.
func newResponseBudget(r io.Reader) *responseBudget {
	b := &responseBudget{r: r}
	b.renew()
	return b
}

// ErrUnsafeCredential refuses a configured user name, password or folder that
// holds a CR, LF or NUL.
var ErrUnsafeCredential = errors.New("fetchmail: a configured value holds a line break or NUL")

// lineSafe refuses any value that would end the command line it is written into.
// Both protocols are line-based and neither quoting form can carry a line break
// (RFC 1939 section 3, RFC 3501 section 4.3 quoted strings), so a CR or LF in a
// stored password would append a second command, such as a delete, to the
// remote session.
func lineSafe(values ...string) error {
	for _, v := range values {
		if strings.ContainsAny(v, "\r\n\x00") {
			return ErrUnsafeCredential
		}
	}
	return nil
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
	return upgradeTLS(raw, serverName, verify)
}

// ErrNoStartTLS reports a plaintext source that would not switch to TLS. The
// credentials are never sent over such a link.
var ErrNoStartTLS = errors.New("fetchmail: source refused STARTTLS; credentials not sent")

// upgradeTLS runs the client handshake over raw, for implicit TLS and for a link
// a STARTTLS or STLS command has just switched. A failed handshake closes raw.
func upgradeTLS(raw net.Conn, serverName string, verify bool) (net.Conn, error) {
	conn := tls.Client(raw, &tls.Config{ServerName: serverName, InsecureSkipVerify: !verify}) // #nosec G402 -- verify is the admin's explicit choice, skipped only when the operator disabled it
	// Bound the handshake: the TCP connect is timed by ssrfguard and post-connect
	// reads by opTimeout, but a source that accepts the connection and then stalls
	// mid-handshake would otherwise block the sequential poll indefinitely and
	// starve every other tenant's fetchmail.
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, errors.Join(err, raw.Close())
	}
	return conn, nil
}

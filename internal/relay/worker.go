package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"hermex/internal/dane"
	"hermex/internal/logging"
	"hermex/internal/mtasts"
)

// Router resolves a recipient domain to the mail exchangers to try, in priority
// order. The default, LookupMX, uses DNS MX records with an address-record
// fallback; tests inject a stub that points every domain at an in-process
// listener.
type Router func(domain string) ([]string, error)

// Dialer opens a connection to a mail exchanger host (the default implies port
// 25). Injected so tests can redirect every host to a local listener.
type Dialer func(host string) (net.Conn, error)

// Worker drains the spool: it claims due recipients, delivers each over SMTP to
// its domain's mail exchanger, and settles the attempt — Sent on success, Retry
// after a transient failure, or give up (settle and bounce) on a permanent
// rejection or once attempts are exhausted.
type Worker struct {
	Spool       *Spool
	HeloName    string        // the name announced in EHLO; "localhost" if empty
	Router      Router        // nil uses LookupMX
	Dialer      Dialer        // nil dials the host on port 25
	Backoff     time.Duration // base delay before a retry; doubles each attempt
	MaxAttempts int           // attempts before giving up; <=0 uses a default
	Batch       int           // max recipients claimed per pass; <=0 uses a default
	// OnGiveUp, if set, is called when a recipient is abandoned (permanent
	// rejection or attempts exhausted) just before it is settled, so the caller
	// can notify the sender. cause is the failure that ended delivery.
	OnGiveUp func(it Item, cause error)
	Logger   *logging.Logger
	// Policy returns the MTA-STS policy for a recipient domain (nil, nil when the
	// domain publishes none); nil disables MTA-STS, leaving every delivery on
	// opportunistic STARTTLS. An enforce-mode policy makes TLS to a policy-matched
	// mail exchanger mandatory and certificate-validated, and excludes any MX host
	// the policy does not list.
	Policy func(domain string) (*mtasts.Policy, error)

	// DANE, when set, authenticates outbound TLS against a mail exchanger's
	// DNSSEC-validated TLSA records (RFC 7672). nil disables DANE, leaving
	// delivery on opportunistic STARTTLS (plus any MTA-STS policy). When a host
	// publishes usable secure TLSA records, TLS becomes mandatory and the server
	// certificate is authenticated against them.
	DANE *dane.Resolver

	// backoffOverride (nanoseconds) and maxAttemptsOverride hold an operator's edited
	// retry policy, set via SetRetryPolicy. They are read atomically by the single Run
	// goroutine so the MTA's poll can apply a change while delivery runs, with no
	// restart; 0 means "fall back to the Backoff/MaxAttempts fields, then the default".
	backoffOverride     atomic.Int64
	maxAttemptsOverride atomic.Int64
}

// SetRetryPolicy installs the operator's retry tuning: the base backoff before the
// first retry (it still doubles per attempt and is capped at maxBackoff) and the
// number of attempts before a recipient is abandoned. It is safe to call concurrently
// with Run, so an edit applies without a restart. A non-positive value for either
// leaves that one falling back to the configured field or the built-in default.
func (w *Worker) SetRetryPolicy(backoff time.Duration, maxAttempts int) {
	if backoff > 0 {
		w.backoffOverride.Store(int64(backoff))
	}
	if maxAttempts > 0 {
		w.maxAttemptsOverride.Store(int64(maxAttempts))
	}
}

const (
	defaultBatch       = 64
	defaultBackoff     = 5 * time.Minute
	defaultMaxAttempts = 10
	maxBackoff         = 6 * time.Hour
	dialTimeout        = 30 * time.Second
	sessionTimeout     = 5 * time.Minute
)

// Run drains the spool on every tick until ctx is cancelled. A scan error is
// logged and the loop continues, so a transient store error does not stop relay.
//
// Exactly one process must run this loop. Claim does not lease the rows it
// returns, so a second concurrent drainer would deliver the same recipient
// twice; like the send-later sweep it lives in the single always-on MTA daemon.
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := w.ProcessDue(time.Now()); err != nil {
			w.Logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.MTA, Name: "relay.scan.fail", Err: err.Error()})
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ProcessDue claims every recipient due at now (up to the batch size) and
// attempts delivery once, settling each. It returns the number delivered. A
// store error settling an item stops the pass and is returned; a delivery
// failure only defers that recipient.
func (w *Worker) ProcessDue(now time.Time) (sent int, err error) {
	batch := w.Batch
	if batch <= 0 {
		batch = defaultBatch
	}
	items, err := w.Spool.Claim(now, batch)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		e := w.deliver(it)
		if e == nil {
			if se := w.Spool.Sent(it.RecipientID); se != nil {
				return sent, se
			}
			sent++
			w.log(logging.LevelInfo, "relay.sent", it, nil)
			continue
		}
		// Give up on a permanent rejection or once attempts are exhausted; the
		// sender is told (OnGiveUp) and the recipient settled. Otherwise defer with
		// an exponential backoff so a transient outage is retried, not lost.
		if isPermanent(e) || it.Attempts+1 >= w.maxAttempts() {
			w.giveUp(it, e)
			if fe := w.Spool.Fail(it.RecipientID); fe != nil {
				return sent, fe
			}
			continue
		}
		if re := w.Spool.Retry(it.RecipientID, now.Add(w.retryDelay(it.Attempts)), e.Error()); re != nil {
			return sent, re
		}
		w.log(logging.LevelWarn, "relay.defer", it, e)
	}
	return sent, nil
}

// giveUp abandons a recipient: it logs loudly and notifies the sender via the
// OnGiveUp hook (if any). The caller settles the recipient afterwards.
func (w *Worker) giveUp(it Item, cause error) {
	w.log(logging.LevelError, "relay.bounce", it, cause)
	if w.OnGiveUp != nil {
		w.OnGiveUp(it, cause)
	}
}

func (w *Worker) maxAttempts() int {
	if n := w.maxAttemptsOverride.Load(); n > 0 {
		return int(n)
	}
	if w.MaxAttempts > 0 {
		return w.MaxAttempts
	}
	return defaultMaxAttempts
}

// retryDelay is the wait before the next attempt: the base backoff doubled once
// per attempt already made, capped so it cannot overflow or grow without bound.
func (w *Worker) retryDelay(attempts int) time.Duration {
	base := time.Duration(w.backoffOverride.Load())
	if base <= 0 {
		base = w.Backoff
	}
	if base <= 0 {
		base = defaultBackoff
	}
	d := base << attempts
	if d <= 0 || d > maxBackoff {
		return maxBackoff
	}
	return d
}

// isPermanent reports whether err is a permanent SMTP rejection (a 5xx reply),
// which must not be retried. Network and 4xx errors are transient.
func isPermanent(err error) bool {
	if terr, ok := errors.AsType[*textproto.Error](err); ok {
		return terr.Code >= 500 && terr.Code < 600
	}
	return false
}

// deliver resolves the recipient's domain to its mail exchangers and tries each
// in priority order until one accepts the message. It returns the last error
// when every host fails.
func (w *Worker) deliver(it Item) error {
	domain := domainPart(it.Recipient)
	if domain == "" {
		return fmt.Errorf("recipient %q has no domain", it.Recipient)
	}
	route := w.Router
	if route == nil {
		route = LookupMX
	}
	hosts, err := route(domain)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", domain, err)
	}
	if len(hosts) == 0 {
		return fmt.Errorf("no mail exchanger for %s", domain)
	}
	// Look up the domain's MTA-STS policy once. A lookup failure does not block
	// mail — it falls back to opportunistic TLS (the pre-MTA-STS behaviour); the
	// resolver's cache is what carries a published policy through a transient
	// policy-host outage, so this fallback only fires before a policy is ever seen.
	var pol *mtasts.Policy
	if w.Policy != nil {
		if pol, err = w.Policy(domain); err != nil {
			w.log(logging.LevelWarn, "mtasts.lookup", it, err)
			pol = nil
		}
	}
	enforce := pol != nil && pol.Mode == mtasts.ModeEnforce
	var lastErr error
	for _, host := range hosts {
		// In enforce mode, deliver only to a mail exchanger the policy lists.
		if enforce && !pol.MatchesMX(host) {
			lastErr = fmt.Errorf("mtasts: %s is not listed in the enforce policy for %s", host, domain)
			continue
		}
		if lastErr = w.send(host, it, enforce); lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// send delivers one message to one mail exchanger over SMTP. When requireTLS is
// set (the recipient's MTA-STS policy is in enforce mode and this host matched
// it), STARTTLS is mandatory and the certificate is validated; otherwise TLS is
// opportunistic.
func (w *Worker) send(host string, it Item, requireTLS bool) error {
	dial := w.Dialer
	if dial == nil {
		dial = dialPort25
	}
	conn, err := dial(host)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(sessionTimeout))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if err := c.Hello(w.heloName(conn.LocalAddr())); err != nil {
		return err
	}
	// DANE (RFC 7672): when a DNSSEC-validating resolver is configured and this
	// mail exchanger publishes usable secure TLSA records, TLS is mandatory and
	// the certificate is authenticated against those records. A TLSA lookup error
	// (e.g. a bogus/SERVFAIL response) fails the host rather than downgrading.
	var daneRecs []dane.Record
	if w.DANE != nil {
		recs, applicable, err := w.DANE.LookupTLSA(host, 25)
		if err != nil {
			return err
		}
		if applicable {
			daneRecs = recs
		}
	}
	// MTA-STS enforce (RFC 8461): the certificate is validated against host (which
	// already matched the policy), and a server that does not offer STARTTLS is
	// refused rather than used in the clear. Otherwise STARTTLS is opportunistic
	// (RFC 7435): encrypt when advertised, accepting any certificate, since the
	// alternative is cleartext and encryption without authentication is still better.
	if ok, _ := c.Extension("STARTTLS"); ok {
		cfg := &tls.Config{ServerName: host, InsecureSkipVerify: !requireTLS}
		if len(daneRecs) > 0 {
			// Authenticate against the TLSA records instead of the PKIX trust
			// store: skip the default verification and match the presented chain
			// in VerifyConnection (RFC 7672 §3).
			cfg = &tls.Config{
				ServerName:         host,
				InsecureSkipVerify: true,
				VerifyConnection: func(cs tls.ConnectionState) error {
					return dane.Match(daneRecs, cs.PeerCertificates, host)
				},
			}
		}
		if err := c.StartTLS(cfg); err != nil {
			return err
		}
	} else if requireTLS {
		return fmt.Errorf("mtasts: %s requires STARTTLS but %s does not offer it", domainPart(it.Recipient), host)
	} else if len(daneRecs) > 0 {
		return fmt.Errorf("dane: %s publishes TLSA records but %s does not offer STARTTLS", domainPart(it.Recipient), host)
	}
	if err := c.Mail(it.From); err != nil {
		return err
	}
	if err := c.Rcpt(it.Recipient); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(it.Body); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// heloName is the name announced in EHLO. The operator's configured HeloName (the
// MTA's FQDN) is preferred. When unset, it falls back to the OS host name, then to
// an address literal of the connection's local address (RFC 5321 §4.1.4: a host
// with no name announces an address literal). Bare "localhost" is a last resort
// only: it is a local alias an SMTP transaction must not carry (RFC 5321 §2.3.5)
// and receiving MTAs routinely reject it, so a real identity is used whenever one
// is available.
func (w *Worker) heloName(local net.Addr) string {
	if w.HeloName != "" {
		return w.HeloName
	}
	if h, err := os.Hostname(); err == nil && h != "" && h != "localhost" {
		return h
	}
	if lit := addressLiteral(local); lit != "" {
		return lit
	}
	return "localhost"
}

// addressLiteral renders a connection's local address as an SMTP address literal
// ("[192.0.2.1]" or "[IPv6:2001:db8::1]", RFC 5321 §4.1.3), or "" when no IP can
// be taken from it (a nil or non-TCP address).
func addressLiteral(addr net.Addr) string {
	ta, ok := addr.(*net.TCPAddr)
	if !ok || ta.IP == nil {
		return ""
	}
	if ta.IP.To4() != nil {
		return "[" + ta.IP.String() + "]"
	}
	return "[IPv6:" + ta.IP.String() + "]"
}

func (w *Worker) log(level logging.Level, name string, it Item, err error) {
	e := logging.Event{
		Level:     level,
		Subsystem: logging.MTA,
		Name:      name,
		User:      it.Recipient,
		Fields:    logging.Fields{"from": it.From, "attempts": it.Attempts},
	}
	if err != nil {
		e.Err = err.Error()
	}
	w.Logger.Emit(e)
}

// dialPort25 is the default Dialer: a bounded-timeout TCP connection to the host
// on the SMTP port.
func dialPort25(host string) (net.Conn, error) {
	return net.DialTimeout("tcp", net.JoinHostPort(host, "25"), dialTimeout)
}

// LookupMX is the default Router: the domain's MX hosts in priority order, or —
// when the domain publishes no usable MX — the domain itself as an implicit mail
// exchanger (RFC 5321 §5.1), provided it has an address record.
func LookupMX(domain string) ([]string, error) {
	mxs, err := net.LookupMX(domain)
	if err == nil && len(mxs) > 0 {
		hosts := orderMX(mxs)
		if len(hosts) == 0 {
			return nil, fmt.Errorf("%s does not accept mail (null MX)", domain)
		}
		return hosts, nil
	}
	if _, e := net.LookupHost(domain); e != nil {
		return nil, fmt.Errorf("no mail exchanger for %s", domain)
	}
	return []string{domain}, nil
}

// orderMX flattens MX records into delivery order: ascending preference (RFC 5321
// §5.1, where lower preference numbers are more preferred), with hosts that share a
// preference shuffled so load spreads across an organization's equal mail
// exchangers (the section's MUST). A lone "." host is a null MX (RFC 7505) and is
// dropped, so an all-null set yields an empty list the caller reports as an error.
func orderMX(mxs []*net.MX) []string {
	sorted := append([]*net.MX(nil), mxs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Pref < sorted[j].Pref })
	hosts := make([]string, 0, len(sorted))
	for i := 0; i < len(sorted); {
		j := i
		for j < len(sorted) && sorted[j].Pref == sorted[i].Pref {
			j++
		}
		group := sorted[i:j]
		rand.Shuffle(len(group), func(a, b int) { group[a], group[b] = group[b], group[a] })
		for _, mx := range group {
			if h := strings.TrimSuffix(mx.Host, "."); h != "" {
				hosts = append(hosts, h)
			}
		}
		i = j
	}
	return hosts
}

// domainPart returns the lowercase domain of an address, or "" when it has none.
func domainPart(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}

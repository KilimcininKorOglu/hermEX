package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"hermex/internal/logging"
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
	if w.MaxAttempts > 0 {
		return w.MaxAttempts
	}
	return defaultMaxAttempts
}

// retryDelay is the wait before the next attempt: the base backoff doubled once
// per attempt already made, capped so it cannot overflow or grow without bound.
func (w *Worker) retryDelay(attempts int) time.Duration {
	base := w.Backoff
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
	var lastErr error
	for _, host := range hosts {
		if lastErr = w.send(host, it); lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// send delivers one message to one mail exchanger over SMTP, upgrading to TLS
// opportunistically.
func (w *Worker) send(host string, it Item) error {
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

	if err := c.Hello(w.heloName()); err != nil {
		return err
	}
	// Opportunistic STARTTLS (RFC 7435): encrypt when the server advertises it,
	// accepting any certificate — the alternative is cleartext, so encryption
	// without authentication is strictly better. Strict verification via
	// DANE/MTA-STS is a later refinement.
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, InsecureSkipVerify: true}); err != nil {
			return err
		}
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

func (w *Worker) heloName() string {
	if w.HeloName != "" {
		return w.HeloName
	}
	return "localhost"
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
		hosts := make([]string, 0, len(mxs))
		for _, mx := range mxs {
			if h := strings.TrimSuffix(mx.Host, "."); h != "" {
				hosts = append(hosts, h) // a lone "." is a null MX (RFC 7505)
			}
		}
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

// domainPart returns the lowercase domain of an address, or "" when it has none.
func domainPart(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}

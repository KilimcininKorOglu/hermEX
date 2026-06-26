// Package notify is the consumer/producer client for the central push relay
// (internal/notifyd). A writer daemon installs a Publisher as the objectstore
// change hook so committed mailbox mutations are POSTed to the relay; a consumer
// daemon holds a Consumer, which streams the relay's events and exposes a per-
// mailbox wake channel its long-polls select on. Both are best-effort: a Publisher
// drops on a full queue, a Consumer reconnects on drop, and neither ever blocks
// the mail path — when the relay is down, consumers simply fall back to polling.
package notify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"hermex/internal/logging"
	"hermex/internal/notifyd"
	"hermex/internal/objectstore"
)

const (
	publishQueue   = 1024                   // producer-side buffer; a full queue drops (the poll cadence is the floor)
	publishTimeout = 5 * time.Second        // per-publish POST deadline (best-effort)
	initialBackoff = 500 * time.Millisecond // consumer reconnect backoff floor
	maxBackoff     = 30 * time.Second       // consumer reconnect backoff ceiling
)

// internalTransport dials the notify daemon, which terminates TLS with a self-
// signed certificate on the internal network (like the other daemons behind the
// gateway). Verification is skipped on this internal hop only — the relay is bound
// internal-only with no host port, and it carries no message content, only wake
// signals — mirroring the gateway→backend transport.
func internalTransport() *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
}

// --- Producer ---

// Publisher ships objectstore change events to the relay. It is installed as the
// objectstore change hook (objectstore.SetChangePublisher(pub.Publish)); Publish is
// non-blocking, handing each event to a background poster so a write path never
// waits on the relay.
type Publisher struct {
	endpoint string
	secret   string
	client   *http.Client
	queue    chan notifyd.Event
}

// NewPublisher starts a Publisher posting to baseURL (the notify_url). An empty
// baseURL yields a nil Publisher, the caller treating that as "push disabled".
func NewPublisher(baseURL, secret string) *Publisher {
	if baseURL == "" {
		return nil
	}
	p := &Publisher{
		endpoint: strings.TrimRight(baseURL, "/") + "/publish",
		secret:   secret,
		client:   &http.Client{Transport: internalTransport(), Timeout: publishTimeout},
		queue:    make(chan notifyd.Event, publishQueue),
	}
	go p.run()
	return p
}

// Publish adapts an objectstore change event onto the wire and enqueues it without
// blocking: a full queue drops the event, since the consumer's poll cadence covers
// any gap. Its signature matches objectstore.SetChangePublisher. A nil Publisher is
// a no-op, so a daemon with push disabled installs nothing.
func (p *Publisher) Publish(ev objectstore.ChangeEvent) {
	if p == nil {
		return
	}
	select {
	case p.queue <- notifyd.Event{Mailbox: ev.MailboxDir, Op: ev.Op, CN: ev.CN, Mid: ev.Mid}:
	default: // queue full — drop (best-effort; the poll cadence is the floor)
	}
}

func (p *Publisher) run() {
	for ev := range p.queue {
		p.post(ev)
	}
}

// post sends one event, swallowing every error: publishing is best-effort, and the
// consumer's poll cadence is the safety net for anything that does not land.
func (p *Publisher) post(ev notifyd.Event) {
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if p.secret != "" {
		req.Header.Set("Authorization", "Bearer "+p.secret)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// --- Consumer ---

// Consumer holds one streaming connection to the relay and routes its events to
// per-mailbox wake channels. A long-poll registers the mailbox it serves and
// selects on the returned channel alongside its existing poll cadence, so it wakes
// the instant the mailbox changes yet still degrades to polling when the relay is
// gone.
type Consumer struct {
	endpoint string
	secret   string
	client   *http.Client
	logger   *logging.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	waiters map[string][]chan struct{} // mailbox dir → wake channels (one mailbox may have several long-polls)
}

// NewConsumer starts a Consumer streaming from baseURL (the notify_url). An empty
// baseURL yields a nil Consumer, which Register treats as "push disabled" (the
// caller falls back to polling). logger may be nil.
func NewConsumer(baseURL, secret string, logger *logging.Logger) *Consumer {
	if baseURL == "" {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Consumer{
		endpoint: strings.TrimRight(baseURL, "/") + "/events",
		secret:   secret,
		client:   &http.Client{Transport: internalTransport()}, // no Timeout: the stream is long-lived
		logger:   logger,
		ctx:      ctx,
		cancel:   cancel,
		waiters:  make(map[string][]chan struct{}),
	}
	go c.run()
	return c
}

// Register subscribes to wake signals for one mailbox directory (a store's Dir()).
// The returned channel receives a value each time that mailbox changes — coalesced,
// since it is buffered size 1, so a burst of changes before the waiter runs becomes
// one wake. The cancel func unregisters; a long-poll MUST defer it. A nil Consumer
// returns a nil channel and a no-op cancel, so a caller writes one select arm that
// is simply never ready when push is disabled.
func (c *Consumer) Register(mailboxDir string) (<-chan struct{}, func()) {
	if c == nil {
		return nil, func() {}
	}
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.waiters[mailboxDir] = append(c.waiters[mailboxDir], ch)
	c.mu.Unlock()
	return ch, func() { c.unregister(mailboxDir, ch) }
}

func (c *Consumer) unregister(mailboxDir string, ch chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.waiters[mailboxDir]
	for i, x := range w {
		if x == ch {
			c.waiters[mailboxDir] = append(w[:i], w[i+1:]...)
			break
		}
	}
	if len(c.waiters[mailboxDir]) == 0 {
		delete(c.waiters, mailboxDir)
	}
}

// Close stops the streaming connection and the reconnect loop.
func (c *Consumer) Close() {
	if c == nil {
		return
	}
	c.cancel()
}

// wake delivers a non-blocking signal to every waiter on a mailbox; a waiter that
// already has a pending wake keeps just the one (coalesced).
func (c *Consumer) wake(mailboxDir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.waiters[mailboxDir] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// wakeAll signals every registered waiter once. It runs after each successful
// (re)connect so any change that landed while the stream was down is observed now
// (the events were missed, but each long-poll's diff catches up).
func (c *Consumer) wakeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, waiters := range c.waiters {
		for _, ch := range waiters {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// run holds the stream open, reconnecting with capped exponential backoff. Backoff
// resets after a connection that actually established, so a transient drop
// reconnects promptly while a daemon that is down is retried ever more slowly.
func (c *Consumer) run() {
	backoff := initialBackoff
	first := true // the first successful connect has no disconnect gap to catch up
	for c.ctx.Err() == nil {
		if c.stream(first) {
			backoff = initialBackoff
			first = false
		} else {
			backoff = min(backoff*2, maxBackoff)
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// stream opens the SSE connection and dispatches events until it drops. It returns
// whether the connection established (a 200), which the caller uses to reset
// backoff. On a reconnect (not the first connect) it fires a catch-up wake before
// reading, since events that landed while the stream was down were missed.
func (c *Consumer) stream(first bool) (connected bool) {
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return false
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if c.logger != nil {
			c.logger.Warn(logging.Notify, "consumer.stream.status", logging.Fields{"status": resp.StatusCode})
		}
		return false
	}
	// On a reconnect, fire a catch-up wake before reading: events that landed while
	// the stream was down were missed, but each waiter's diff catches up. The first
	// connect has no such gap (the long-polls' own cadence covers startup).
	if !first {
		c.wakeAll()
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev notifyd.Event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		c.wake(ev.Mailbox)
	}
	// The scan ended (the stream dropped); the reason is informational only — the
	// reconnect loop handles it either way.
	if err := sc.Err(); err != nil && c.logger != nil {
		c.logger.Debug(logging.Notify, "consumer.stream.end", logging.Fields{"err": err.Error()})
	}
	return true
}

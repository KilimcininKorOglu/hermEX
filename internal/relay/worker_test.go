package relay

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"hermex/internal/mtasts"
	hsmtp "hermex/internal/smtp"
)

// recordingBackend is an in-process SMTP sink: it accepts a message and records
// its envelope and body, standing in for a remote mail exchanger so the relay
// path can be exercised end to end without the network.
type recordingBackend struct {
	mu      sync.Mutex
	msgs    []recordedMsg
	rcptErr error // when set, RCPT is refused (the server replies 5xx)
}

type recordedMsg struct {
	from string
	rcpt []string
	data []byte
}

func (b *recordingBackend) NewSession(string) (hsmtp.Session, error) {
	return &recordingSession{b: b}, nil
}

func (b *recordingBackend) recorded() []recordedMsg {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]recordedMsg(nil), b.msgs...)
}

type recordingSession struct {
	b   *recordingBackend
	cur recordedMsg
}

func (s *recordingSession) Mail(from string) error { s.cur.from = from; return nil }
func (s *recordingSession) Rcpt(to string) error {
	if s.b.rcptErr != nil {
		return s.b.rcptErr
	}
	s.cur.rcpt = append(s.cur.rcpt, to)
	return nil
}
func (s *recordingSession) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.cur.data = raw
	s.b.mu.Lock()
	s.b.msgs = append(s.b.msgs, s.cur)
	s.b.mu.Unlock()
	return nil
}
func (s *recordingSession) Reset()        { s.cur = recordedMsg{} }
func (s *recordingSession) Logout() error { return nil }

func startSink(t *testing.T) (*recordingBackend, string) {
	t.Helper()
	be := &recordingBackend{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go (&hsmtp.Server{Backend: be, Hostname: "sink.test"}).Serve(ln)
	return be, ln.Addr().String()
}

// TestWorkerDeliversToSink proves the worker drains a queued recipient: it
// resolves the route, opens an SMTP session to the (injected) mail exchanger,
// transmits the message faithfully, and settles the recipient so the spool
// empties.
func TestWorkerDeliversToSink(t *testing.T) {
	sink, addr := startSink(t)
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	raw := []byte("From: alice@local\r\nSubject: out\r\n\r\nhi bob\r\n")
	if err := sp.Enqueue("alice@local", []string{"bob@remote"}, raw, t0); err != nil {
		t.Fatal(err)
	}

	w := &Worker{
		Spool:    sp,
		HeloName: "mx.test",
		Router:   func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:   func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
	}
	sent, err := w.ProcessDue(t0)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if sent != 1 {
		t.Fatalf("delivered %d, want 1", sent)
	}

	msgs := sink.recorded()
	if len(msgs) != 1 {
		t.Fatalf("sink recorded %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.from != "alice@local" {
		t.Errorf("sink MAIL FROM = %q, want alice@local", m.from)
	}
	if len(m.rcpt) != 1 || m.rcpt[0] != "bob@remote" {
		t.Errorf("sink RCPT = %v, want [bob@remote]", m.rcpt)
	}
	if !bytes.Contains(m.data, []byte("hi bob")) {
		t.Errorf("sink body missing payload: %q", m.data)
	}
	if due, _ := sp.Claim(t0, 10); len(due) != 0 {
		t.Errorf("spool not drained after delivery: %v", due)
	}
}

// TestWorkerMTASTSEnforceSkipsUnlistedMX proves an enforce-mode policy excludes a
// mail exchanger it does not list: the worker never opens a session to an MX
// outside the policy, which is the downgrade resistance MTA-STS exists for.
func TestWorkerMTASTSEnforceSkipsUnlistedMX(t *testing.T) {
	sink, addr := startSink(t)
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	if err := sp.Enqueue("alice@local", []string{"bob@remote"}, []byte("hi\r\n"), t0); err != nil {
		t.Fatal(err)
	}
	w := &Worker{
		Spool:  sp,
		Router: func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer: func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
		Policy: func(string) (*mtasts.Policy, error) {
			return &mtasts.Policy{Mode: mtasts.ModeEnforce, MX: []string{"approved.mx"}}, nil
		},
	}
	if _, err := w.ProcessDue(t0); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := len(sink.recorded()); got != 0 {
		t.Errorf("delivered to an unlisted MX %d time(s), want 0", got)
	}
}

// TestWorkerMTASTSEnforceRequiresTLS proves that to a policy-listed MX offering no
// STARTTLS, enforce mode refuses to deliver in the clear (the sink here advertises
// no STARTTLS, so a delivered message would mean a downgrade).
func TestWorkerMTASTSEnforceRequiresTLS(t *testing.T) {
	sink, addr := startSink(t)
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	if err := sp.Enqueue("alice@local", []string{"bob@remote"}, []byte("hi\r\n"), t0); err != nil {
		t.Fatal(err)
	}
	w := &Worker{
		Spool:  sp,
		Router: func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer: func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
		Policy: func(string) (*mtasts.Policy, error) {
			return &mtasts.Policy{Mode: mtasts.ModeEnforce, MX: []string{"sink"}}, nil
		},
	}
	if _, err := w.ProcessDue(t0); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := len(sink.recorded()); got != 0 {
		t.Errorf("delivered in the clear to a STARTTLS-less MX %d time(s), want 0", got)
	}
}

// TestWorkerRetriesTransientFailure proves a failed delivery defers the
// recipient by the backoff rather than losing it, and the next pass after the
// backoff elapses delivers it. This is the path most likely to be wrong, so it
// is exercised with a dialer that fails once then succeeds.
func TestWorkerRetriesTransientFailure(t *testing.T) {
	sink, addr := startSink(t)
	sp := openSpool(t)
	t0 := time.Unix(4_000_000, 0)
	if err := sp.Enqueue("alice@local", []string{"bob@remote"}, []byte("raw body\r\n"), t0); err != nil {
		t.Fatal(err)
	}

	var calls int
	w := &Worker{
		Spool:   sp,
		Backoff: time.Hour,
		Router:  func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer: func(string) (net.Conn, error) {
			calls++
			if calls == 1 {
				return nil, fmt.Errorf("connection refused")
			}
			return net.Dial("tcp", addr)
		},
	}

	// First pass: the dial fails, so the recipient is deferred, not delivered.
	if sent, err := w.ProcessDue(t0); err != nil || sent != 0 {
		t.Fatalf("first pass: sent=%d err=%v, want 0, nil", sent, err)
	}
	if len(sink.recorded()) != 0 {
		t.Fatal("nothing should reach the sink on a failed dial")
	}
	// It is not due again until the backoff elapses.
	if sent, _ := w.ProcessDue(t0.Add(time.Minute)); sent != 0 {
		t.Errorf("a deferred recipient was delivered before its backoff elapsed")
	}
	// After the backoff, the retry succeeds.
	sent, err := w.ProcessDue(t0.Add(time.Hour))
	if err != nil {
		t.Fatalf("retry pass: %v", err)
	}
	if sent != 1 {
		t.Fatalf("retry delivered %d, want 1", sent)
	}
	if got := sink.recorded(); len(got) != 1 || got[0].rcpt[0] != "bob@remote" {
		t.Fatalf("sink after retry = %v, want one message to bob@remote", got)
	}
}

// TestWorkerBouncesPermanentFailure proves a permanent (5xx) rejection is not
// retried: the recipient is abandoned at once, the sender is notified through
// OnGiveUp, and the spool empties.
func TestWorkerBouncesPermanentFailure(t *testing.T) {
	sink, addr := startSink(t)
	sink.rcptErr = fmt.Errorf("mailbox does not exist") // the sink replies 5xx to RCPT
	sp := openSpool(t)
	t0 := time.Unix(5_000_000, 0)
	if err := sp.Enqueue("alice@local", []string{"bob@remote"}, []byte("raw\r\n"), t0); err != nil {
		t.Fatal(err)
	}

	var bounced []Item
	w := &Worker{
		Spool:    sp,
		Router:   func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:   func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
		OnGiveUp: func(it Item, _ error) { bounced = append(bounced, it) },
	}

	if sent, err := w.ProcessDue(t0); err != nil || sent != 0 {
		t.Fatalf("permanent failure: sent=%d err=%v, want 0, nil", sent, err)
	}
	if len(bounced) != 1 || bounced[0].Recipient != "bob@remote" {
		t.Fatalf("OnGiveUp fired %v, want once for bob@remote", bounced)
	}
	if due, _ := sp.Claim(t0, 10); len(due) != 0 {
		t.Errorf("a permanently-failed recipient was left in the spool: %v", due)
	}
}

// TestWorkerGivesUpAfterMaxAttempts proves a recipient that keeps failing
// transiently is eventually abandoned rather than retried forever: with two
// attempts allowed, the second failure bounces it.
func TestWorkerGivesUpAfterMaxAttempts(t *testing.T) {
	sp := openSpool(t)
	t0 := time.Unix(6_000_000, 0)
	if err := sp.Enqueue("alice@local", []string{"bob@remote"}, []byte("raw\r\n"), t0); err != nil {
		t.Fatal(err)
	}

	var bounced []Item
	w := &Worker{
		Spool:       sp,
		Backoff:     time.Minute,
		MaxAttempts: 2,
		Router:      func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:      func(string) (net.Conn, error) { return nil, fmt.Errorf("connection refused") },
		OnGiveUp:    func(it Item, _ error) { bounced = append(bounced, it) },
	}

	// First attempt: transient failure, deferred.
	if sent, _ := w.ProcessDue(t0); sent != 0 {
		t.Fatal("first attempt should not deliver")
	}
	if len(bounced) != 0 {
		t.Fatal("a recipient was abandoned before its attempts were exhausted")
	}
	// Second attempt, after the backoff: attempts are now exhausted, so it bounces.
	if sent, _ := w.ProcessDue(t0.Add(time.Minute)); sent != 0 {
		t.Fatal("second attempt should not deliver")
	}
	if len(bounced) != 1 || bounced[0].Recipient != "bob@remote" {
		t.Fatalf("OnGiveUp fired %v, want once after attempts exhausted", bounced)
	}
	if due, _ := sp.Claim(t0.Add(time.Hour), 10); len(due) != 0 {
		t.Errorf("an exhausted recipient was left in the spool: %v", due)
	}
}

package antispam

import (
	"context"
	"net"
	"testing"
	"time"
)

// hangingResolver answers nothing at all, the shape of a nameserver a sender
// controls and simply points into a hole. Every dial blocks until the lookup's own
// deadline cancels it, so a check with no deadline of its own never returns.
func hangingResolver() *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
}

// boundedChecks points the checks at a nameserver that never answers and shortens
// their deadlines so the test does not spend the real budget waiting.
func boundedChecks(t *testing.T, d time.Duration) {
	t.Helper()
	oldResolver, oldDNS, oldSPF := resolver, dnsTimeout, spfTimeout
	resolver, dnsTimeout, spfTimeout = hangingResolver(), d, d
	t.Cleanup(func() { resolver, dnsTimeout, spfTimeout = oldResolver, oldDNS, oldSPF })
}

// mustReturnWithin runs fn and fails if it has not finished by limit. A check with
// no deadline never finishes here at all, so the failure is the bug.
func mustReturnWithin(t *testing.T, limit time.Duration, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Errorf("%s did not return within %s, so it holds the SMTP connection's goroutine", name, limit)
	}
}

// TestSPFIsBounded proves an SPF evaluation cannot be stalled by the sender. The
// domain queried comes from the client's own HELO or MAIL FROM, so its nameserver
// is one the sender may control, and RFC 7208 lets a single evaluation chase up to
// ten records against it. The check runs inline in the DATA phase before the 250,
// on a listener with no connection cap.
func TestSPFIsBounded(t *testing.T) {
	boundedChecks(t, 200*time.Millisecond)
	var got AuthResult
	mustReturnWithin(t, 5*time.Second, "realSPF", func() {
		got = realSPF(net.ParseIP("192.0.2.1"), "sender.invalid", "someone@sender.invalid")
	})
	// Timing out is a resolution failure, which is the advisory answer SPF already
	// gives for any other one. It must not read as a pass.
	if got == AuthPass {
		t.Errorf("an unanswerable SPF lookup reported %v", got)
	}
}

// TestDMARCIsBounded proves the same for the DMARC policy lookup, which queries
// _dmarc under the From-header domain. It used to call the library default, which
// resolves with a bare net.LookupTXT and carries no deadline at all.
func TestDMARCIsBounded(t *testing.T) {
	boundedChecks(t, 200*time.Millisecond)
	var policy string
	var ok bool
	mustReturnWithin(t, 5*time.Second, "realDMARC", func() {
		policy, ok = realDMARC("sender.invalid")
	})
	if ok {
		t.Errorf("an unanswerable DMARC lookup reported a policy %q", policy)
	}
}

// TestDNSBLIsBounded proves the same per zone. The scorer checks the configured
// zones in sequence, so an unbounded query is multiplied by the number of zones on
// every message.
func TestDNSBLIsBounded(t *testing.T) {
	boundedChecks(t, 200*time.Millisecond)
	var listed bool
	mustReturnWithin(t, 5*time.Second, "realDNSBL", func() {
		listed = realDNSBL(net.ParseIP("192.0.2.1"), "bl.example.invalid")
	})
	// DNSBL is fail-open: unreachable must never read as listed, or an outage files
	// legitimate mail to Junk.
	if listed {
		t.Error("an unanswerable DNSBL lookup reported the client as listed")
	}
}

// TestScoreIsBoundedWithUnanswerableDNS is the end-to-end statement: one message
// scored against a nameserver that answers nothing still returns, rather than
// pinning the connection's goroutine for the resolver's full retry budget across
// every check.
func TestScoreIsBoundedWithUnanswerableDNS(t *testing.T) {
	boundedChecks(t, 200*time.Millisecond)
	s := New(DefaultWeights, DefaultThreshold)
	// Zones make the DNSBL loop run too, so the end-to-end bound covers every
	// unanswerable query a message triggers, not just the first.
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: DefaultThreshold,
		Zones: []string{"bl1.example.invalid", "bl2.example.invalid"}})
	in := Input{
		ClientIP:   net.ParseIP("192.0.2.1"),
		HeloName:   "sender.invalid",
		MailFrom:   "someone@sender.invalid",
		FromDomain: "sender.invalid",
		Raw:        []byte("Subject: hi\r\n\r\nbody\r\n"),
	}
	mustReturnWithin(t, 10*time.Second, "Score", func() { s.Score(in) })
}

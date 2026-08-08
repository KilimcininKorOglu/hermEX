package smtp

import (
	"strings"
	"testing"
	"time"
)

// stamped is the trace header for one connection, with the given reverse-DNS name.
func stamped(rdns string) string {
	return buildReceived("client.example", "203.0.113.9:2525", rdns, "mail.hermex.test", false,
		time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
}

// TestReceivedRejectsInjectedReverseDNS proves a PTR record cannot add header
// lines. The record is free-form data published by whoever controls the sending
// IP's rDNS, so it must not be trusted to stay on one line.
func TestReceivedRejectsInjectedReverseDNS(t *testing.T) {
	got := stamped("evil.example\r\nX-Injected: header")

	if strings.Contains(got, "X-Injected") {
		t.Errorf("the injected text reached the header:\n%q", got)
	}
	// Exactly three lines: the header and its two intentional continuations.
	if n := strings.Count(got, "\r\n"); n != 3 {
		t.Errorf("header has %d lines, want 3:\n%q", n, got)
	}
	if !strings.Contains(got, "(unknown [203.0.113.9])") {
		t.Errorf("an unusable name must be reported as unknown:\n%q", got)
	}
}

// TestReceivedKeepsOrdinaryReverseDNS keeps the trace informative for the normal
// case, which is the whole point of the lookup.
func TestReceivedKeepsOrdinaryReverseDNS(t *testing.T) {
	got := stamped("mx1.sender.example")
	if !strings.Contains(got, "(mx1.sender.example [203.0.113.9])") {
		t.Errorf("the resolved name is missing:\n%q", got)
	}
}

// TestPrintableName covers the predicate directly, including the bytes that end a
// header line and the ones that would break the header's own syntax.
func TestPrintableName(t *testing.T) {
	for _, bad := range []string{"a\rb", "a\nb", "a b", "a\tb", "a\x00b", "hös.example"} {
		if printableName(bad) {
			t.Errorf("printableName(%q) = true, want false", bad)
		}
	}
	for _, good := range []string{"mx1.sender.example", "xn--rnek-qua.example", "host-1.a.b"} {
		if !printableName(good) {
			t.Errorf("printableName(%q) = false, want true", good)
		}
	}
}

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
	for _, rdns := range []string{
		"evil.example\r\nX-Injected: header",
		"evil.example\rX-Injected: header",
		"evil.example\x00x",
		"two words",
		"evil\x7f.example",
		"hös.example",
	} {
		got := stamped(rdns)
		if strings.Contains(got, "X-Injected") {
			t.Errorf("rDNS %q: injected text reached header:\n%q", rdns, got)
		}
		if strings.ContainsAny(strings.ReplaceAll(got, "\r\n", ""), "\r\n\x00\x7f") {
			t.Errorf("rDNS %q: control byte survived in header:\n%q", rdns, got)
		}
		if n := strings.Count(got, "\r\n"); n != 3 {
			t.Errorf("rDNS %q: header has %d lines, want 3:\n%q", rdns, n, got)
		}
		if !strings.Contains(got, "(unknown [203.0.113.9])") {
			t.Errorf("rDNS %q: unusable name must be reported as unknown:\n%q", rdns, got)
		}
	}
}

// TestReceivedRejectsAControlByteInHELO feeds the HELO argument the command reader
// hands over for "EHLO evil.example\rX-Injected: header\r\n": the line ends at the
// LF, so the bare CR stays in the argument. RFC 5322 2.2 allows no CR or LF in a
// field outside a CRLF, and a reader that takes the CR as a line end sees a header
// field the client wrote. The client's name must stay on its line like the PTR
// name, and an argument that is not a printable name is reported as unknown.
func TestReceivedRejectsAControlByteInHELO(t *testing.T) {
	for _, helo := range []string{"evil.example\rX-Injected: header", "evil.example\x00x", "two words", "bad\x7f.example"} {
		got := buildReceived(helo, "203.0.113.9:2525", "mx.sender.example", "mail.hermex.test", false,
			time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
		if strings.ContainsAny(strings.ReplaceAll(got, "\r\n", ""), "\r\n\x00") || strings.Contains(got, "X-Injected") {
			t.Errorf("HELO %q reached the header:\n%q", helo, got)
		}
		if !strings.HasPrefix(got, "Received: from unknown (") {
			t.Errorf("HELO %q: header = %q, want the client name reported as unknown", helo, got)
		}
	}
	if got := buildReceived("[203.0.113.9]", "203.0.113.9:2525", "", "mail.hermex.test", false, time.Now()); !strings.HasPrefix(got, "Received: from [203.0.113.9] (") {
		t.Errorf("an address-literal HELO was not kept: %q", got)
	}
}

// TestReceivedKeepsOrdinaryReverseDNS keeps the trace informative for normal
// IPv4/IPv6 addresses with and without ports.
func TestReceivedKeepsOrdinaryReverseDNS(t *testing.T) {
	got := stamped("mx1.sender.example")
	if !strings.Contains(got, "(mx1.sender.example [203.0.113.9])") {
		t.Errorf("the resolved name is missing:\n%q", got)
	}
	bareV4 := buildReceived("client.example", "203.0.113.9", "mx1.sender.example", "mail.hermex.test", false, time.Unix(0, 0).UTC())
	if !strings.Contains(bareV4, "(mx1.sender.example [203.0.113.9])") {
		t.Errorf("bare IPv4 header = %q", bareV4)
	}
	bareV6 := buildReceived("client.example", "2001:db8::1", "mx1.sender.example", "mail.hermex.test", true, time.Unix(0, 0).UTC())
	if !strings.Contains(bareV6, "(mx1.sender.example [IPv6:2001:db8::1])") || !strings.Contains(bareV6, "with SMTPS;") {
		t.Errorf("bare IPv6 TLS header = %q", bareV6)
	}
}

// TestPrintableName covers the predicate directly, including the bytes that end a
// header line and the ones that would break the header's own syntax.
func TestPrintableName(t *testing.T) {
	for _, bad := range []string{"a\rb", "a\nb", "a b", "a\tb", "a\x00b", "a\x1fb", "a\x0bb", "a\x7fb", "hös.example"} {
		if printableName(bad) {
			t.Errorf("printableName(%q) = true, want false", bad)
		}
	}
	for _, good := range []string{"mx1.sender.example", "xn--rnek-qua.example", "host-1.a.b", "[203.0.113.9]"} {
		if !printableName(good) {
			t.Errorf("printableName(%q) = false, want true", good)
		}
	}
}

package antispam

import (
	"strings"
	"testing"
)

// TestStripOwnAuthResultsRemovesOnlyOurs strips the fields a sender forged under
// this server's authserv-id and keeps everything else. RFC 8601 section 5 makes
// the border MTA remove results claiming its own name, in any case and whether
// or not a version follows the id, including continuation lines.
func TestStripOwnAuthResultsRemovesOnlyOurs(t *testing.T) {
	raw := "Authentication-Results: mx.hermex.test; dkim=pass header.d=bank.example\r\n" +
		"authentication-results:MX.HERMEX.TEST 1;\r\n\tspf=pass smtp.mailfrom=x@bank.example\r\n" +
		"Authentication-Results: mx.hermex.test\r\n" +
		"Authentication-Results: relay.other.example; spf=fail\r\n" +
		"Subject: hi\r\n\r\nAuthentication-Results: mx.hermex.test; body text\r\n"
	got := string(StripOwnAuthResults([]byte(raw), "mx.hermex.test"))
	head, body, found := strings.Cut(got, "\r\n\r\n")
	if !found {
		t.Fatalf("stripped message missing header/body separator: %q", got)
	}
	if strings.Contains(strings.ToLower(head), "mx.hermex.test") || strings.Contains(head, "spf=pass") {
		t.Errorf("a forged field under our authserv-id survived:\n%s", head)
	}
	if !strings.Contains(head, "relay.other.example; spf=fail") || !strings.Contains(head, "Subject: hi") {
		t.Errorf("another server's field or an ordinary field was removed:\n%s", head)
	}
	if body != "Authentication-Results: mx.hermex.test; body text\r\n" {
		t.Errorf("the body changed: %q", body)
	}
	if got := StripOwnAuthResults([]byte(raw), ""); string(got) != raw {
		t.Error("an empty authserv-id removed fields")
	}
}

// TestAuthResultsFieldReportsTheChecks builds the field for a verdict. Each
// method appears with its RFC 8601 result and property: spf with
// smtp.mailfrom, dkim with header.d for each verified domain, dmarc with
// header.from. A lookup error is reported as temperror (section 2.7).
func TestAuthResultsFieldReportsTheChecks(t *testing.T) {
	v := Verdict{SPF: AuthPass, DKIM: AuthPass, DKIMDomains: []string{"bank.example", "esp.example"}, DMARC: AuthError}
	f := AuthResultsField("mx.hermex.test", v, "x@bank.example", "bank.example")
	if !strings.HasPrefix(f, "Authentication-Results: mx.hermex.test;") || !strings.HasSuffix(f, "\r\n") {
		t.Fatalf("field = %q", f)
	}
	for _, want := range []string{
		"spf=pass smtp.mailfrom=x@bank.example",
		"dkim=pass header.d=bank.example",
		"dkim=pass header.d=esp.example",
		"dmarc=temperror header.from=bank.example",
	} {
		if !strings.Contains(f, want) {
			t.Errorf("field lacks %q: %s", want, f)
		}
	}
	none := AuthResultsField("mx.hermex.test", Verdict{SPF: AuthNone, DKIM: AuthFail, DMARC: AuthNone}, "", "")
	if !strings.Contains(none, "dkim=fail") {
		t.Errorf("an unverified message did not report dkim=fail: %s", none)
	}
}

// TestStripOwnAuthResultsHostileShapes checks space-before-colon, bare LF,
// prefix-similar foreign authserv-ids, and input slice immutability (RFC 8601 5).
func TestStripOwnAuthResultsHostileShapes(t *testing.T) {
	raw := []byte("Authentication-Results : mx.hermex.test; spf=pass\n" +
		"Authentication-Results: mx.hermex.test.evil.example; spf=pass\n" +
		"Subject: hi\n\nbody\n")
	orig := append([]byte(nil), raw...)
	got := string(StripOwnAuthResults(raw, "mx.hermex.test"))
	if string(raw) != string(orig) {
		t.Errorf("StripOwnAuthResults mutated input slice")
	}
	want := "Authentication-Results: mx.hermex.test.evil.example; spf=pass\nSubject: hi\n\nbody\n"
	wantEq(t, got, want, "stripped hostile Authentication-Results")
}

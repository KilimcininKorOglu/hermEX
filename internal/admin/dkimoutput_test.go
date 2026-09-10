package admin

import (
	"strings"
	"testing"
)

// TestDKIMOutputRecordQuotesShortValue proves a value that fits one character string is
// quoted on its own, without the parentheses a multi-string record needs.
func TestDKIMOutputRecordQuotesShortValue(t *testing.T) {
	got := dkimOutput(dkimOutputRecord, "hermex._domainkey.acme.test", "v=DKIM1; k=ed25519; p=AAA")

	want := `hermex._domainkey.acme.test. IN TXT "v=DKIM1; k=ed25519; p=AAA"`
	if got != want {
		t.Errorf("record = %q, want %q", got, want)
	}
}

// TestDKIMOutputRecordSplitsLongValue is the load-bearing case: an RSA record value is
// roughly 390 characters and a DNS character string holds at most 255, so a zone-file line
// must break it into several quoted strings or the zone fails to load.
func TestDKIMOutputRecordSplitsLongValue(t *testing.T) {
	value := "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 400)

	got := dkimOutput(dkimOutputRecord, "hermex._domainkey.acme.test", value)

	if strings.Count(got, `"`) != 4 {
		t.Errorf("a 418-character value must become two quoted strings, got:\n%s", got)
	}
}

// TestDKIMOutputRecordWrapsSplitValue proves a multi-string record carries the parentheses
// a zone file requires around it.
func TestDKIMOutputRecordWrapsSplitValue(t *testing.T) {
	value := "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 400)

	got := dkimOutput(dkimOutputRecord, "hermex._domainkey.acme.test", value)

	if !strings.Contains(got, "( ") || !strings.HasSuffix(got, ")") {
		t.Errorf("a multi-string record must be parenthesized, got:\n%s", got)
	}
}

// TestDKIMOutputRecordKeepsEveryByte proves splitting loses nothing: the quoted strings
// concatenate back to the record value a resolver would rebuild.
func TestDKIMOutputRecordKeepsEveryByte(t *testing.T) {
	value := "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 400)

	got := dkimOutput(dkimOutputRecord, "hermex._domainkey.acme.test", value)

	if rejoined := strings.ReplaceAll(got, `" "`, ""); !strings.Contains(rejoined, value) {
		t.Errorf("the quoted strings must concatenate back to the record value, got:\n%s", got)
	}
}

// TestDKIMOutputUnknownModeFallsBackToRecord proves an unrecognized mode serves the
// zone-file line the panel opens in, rather than an empty box.
func TestDKIMOutputUnknownModeFallsBackToRecord(t *testing.T) {
	got := dkimOutput("nonsense", "hermex._domainkey.acme.test", "v=DKIM1; k=rsa; p=AAA")

	if !strings.HasPrefix(got, "hermex._domainkey.acme.test. IN TXT ") {
		t.Errorf("output = %q, want the zone-file line", got)
	}
}

// TestDKIMOutputWithoutKeyIsEmpty proves a domain with no key renders nothing, so the
// panel never shows a record line built around an empty value.
func TestDKIMOutputWithoutKeyIsEmpty(t *testing.T) {
	if got := dkimOutput(dkimOutputRecord, "hermex._domainkey.acme.test", ""); got != "" {
		t.Errorf("output = %q, want an empty string", got)
	}
}

// TestNormalizeSelectorLowerCases proves a selector is stored lower-cased, because DNS
// lookups are case-insensitive and a mixed-case stored value would not match the record
// name the check queries.
func TestNormalizeSelectorLowerCases(t *testing.T) {
	got, ok := normalizeSelector("Mail2026")
	if !ok {
		t.Fatal("Mail2026 must be a valid selector")
	}
	if got != "mail2026" {
		t.Errorf("selector = %q, want mail2026", got)
	}
}

// TestNormalizeSelectorAcceptsDottedLabels proves the dotted form RFC 6376 allows is
// accepted, not just a single label.
func TestNormalizeSelectorAcceptsDottedLabels(t *testing.T) {
	if _, ok := normalizeSelector("march.2026"); !ok {
		t.Error("a dot-separated selector must be accepted")
	}
}

// TestNormalizeSelectorRejectsBadValues covers the shapes that cannot be a DNS label.
func TestNormalizeSelectorRejectsBadValues(t *testing.T) {
	for _, sel := range []string{"has space", "under_score", "-lead", "trail-", "", "a..b", strings.Repeat("a", 64)} {
		if _, ok := normalizeSelector(sel); ok {
			t.Errorf("selector %q must be rejected", sel)
		}
	}
}

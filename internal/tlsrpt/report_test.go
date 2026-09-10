package tlsrpt

import (
	"encoding/json"
	"testing"
	"time"
)

// TestReportJSON proves the report serialises to the exact field names RFC 8460
// §4.4 defines (the wire contract a receiver parses), that the UTC date range is
// rendered in RFC 3339 form, and that an empty failure-details list is omitted
// while a populated one carries the result type and count.
func TestReportJSON(t *testing.T) {
	rep := &Report{
		OrganizationName: "Company-X",
		DateRange: DateRange{
			Start: time.Date(2016, 4, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2016, 4, 1, 23, 59, 59, 0, time.UTC),
		},
		ContactInfo: "sts-reporting@company-x.example",
		ReportID:    "5065427c-23d3-47ca-b6e0-946ea0e8c4be",
		Policies: []PolicyResult{{
			Policy: PolicyDescriptor{
				PolicyType:   PolicyTypeSTS,
				PolicyDomain: "company-y.example",
				MXHost:       "*.mail.company-y.example",
			},
			Summary: Summary{TotalSuccessful: 5326, TotalFailure: 100},
			FailureDetails: []FailureDetail{{
				ResultType:          ResultCertificateExpired,
				ReceivingMXHostname: "mx1.mail.company-y.example",
				FailedSessionCount:  100,
			}},
		}},
	}

	// Decode into a generic map to assert the wire field names, not Go names.
	m := reportMap(t, rep)
	for _, key := range []string{"organization-name", "date-range", "contact-info", "report-id", "policies"} {
		wantField(t, m, key)
	}
	dr := m["date-range"].(map[string]any)
	wantValue(t, dr["start-datetime"], "2016-04-01T00:00:00Z", "start-datetime (RFC 3339 UTC)")

	pol := m["policies"].([]any)[0].(map[string]any)
	policy := pol["policy"].(map[string]any)
	wantValue(t, policy["policy-type"], PolicyTypeSTS, "policy-type")
	// policy-string is empty here and must be omitted, not rendered as null.
	wantNoField(t, policy, "policy-string")

	summary := pol["summary"].(map[string]any)
	wantValue(t, summary["total-successful-session-count"], 5326.0, "total-successful-session-count")
	fd := pol["failure-details"].([]any)[0].(map[string]any)
	wantValue(t, fd["result-type"], ResultCertificateExpired, "result-type")
	wantValue(t, fd["failed-session-count"], 100.0, "failed-session-count")
}

// reportMap renders a report and decodes it into a generic map, so an assertion
// names the wire field rather than the Go field.
func reportMap(t *testing.T, rep *Report) map[string]any {
	t.Helper()
	b, err := rep.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// wantField fails the test unless the object carries the field.
func wantField(t *testing.T, m map[string]any, key string) {
	t.Helper()
	if _, ok := m[key]; !ok {
		t.Errorf("report JSON missing field %q", key)
	}
}

// wantNoField fails the test unless the field is omitted entirely.
func wantNoField(t *testing.T, m map[string]any, key string) {
	t.Helper()
	if _, ok := m[key]; ok {
		t.Errorf("empty %s must be omitted from the JSON", key)
	}
}

// wantValue fails the test unless the decoded field equals what the caller expected.
func wantValue(t *testing.T, got, want any, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// TestReportFailureDetailsOmitted proves a policy block with no failures omits
// the failure-details array entirely (omitempty), keeping an all-success report
// compact.
func TestReportFailureDetailsOmitted(t *testing.T) {
	rep := &Report{
		Policies: []PolicyResult{{
			Policy:  PolicyDescriptor{PolicyType: PolicyTypeNoPolicy, PolicyDomain: "d.example"},
			Summary: Summary{TotalSuccessful: 10},
		}},
	}
	b, err := rep.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	pol := m["policies"].([]any)[0].(map[string]any)
	if _, ok := pol["failure-details"]; ok {
		t.Error("a policy with no failures must omit failure-details")
	}
}

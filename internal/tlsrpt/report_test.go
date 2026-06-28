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

	b, err := rep.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	// Decode into a generic map to assert the wire field names, not Go names.
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"organization-name", "date-range", "contact-info", "report-id", "policies"} {
		if _, ok := m[key]; !ok {
			t.Errorf("report JSON missing field %q", key)
		}
	}
	dr := m["date-range"].(map[string]any)
	if got := dr["start-datetime"]; got != "2016-04-01T00:00:00Z" {
		t.Errorf("start-datetime = %v, want RFC 3339 UTC 2016-04-01T00:00:00Z", got)
	}

	pol := m["policies"].([]any)[0].(map[string]any)
	policy := pol["policy"].(map[string]any)
	if policy["policy-type"] != PolicyTypeSTS {
		t.Errorf("policy-type = %v, want %q", policy["policy-type"], PolicyTypeSTS)
	}
	// policy-string is empty here and must be omitted, not rendered as null.
	if _, ok := policy["policy-string"]; ok {
		t.Error("empty policy-string must be omitted from the JSON")
	}
	summary := pol["summary"].(map[string]any)
	if summary["total-successful-session-count"].(float64) != 5326 {
		t.Errorf("total-successful-session-count = %v, want 5326", summary["total-successful-session-count"])
	}
	fd := pol["failure-details"].([]any)[0].(map[string]any)
	if fd["result-type"] != ResultCertificateExpired {
		t.Errorf("result-type = %v, want %q", fd["result-type"], ResultCertificateExpired)
	}
	if fd["failed-session-count"].(float64) != 100 {
		t.Errorf("failed-session-count = %v, want 100", fd["failed-session-count"])
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

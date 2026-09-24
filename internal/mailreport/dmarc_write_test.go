package mailreport

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestAggregateXMLRoundTrip proves a report this server writes reads back, through
// the parser it applies to received reports, as the same report. A field lost on
// the way out would be missing from every report a receiver gets.
func TestAggregateXMLRoundTrip(t *testing.T) {
	in := &Aggregate{
		OrgName: "mail.hermex.test", Email: "postmaster@mail.hermex.test", ReportID: "1700000000.example.test@mail.hermex.test",
		Begin: time.Unix(1_699_920_000, 0).UTC(), End: time.Unix(1_700_006_399, 0).UTC(),
		Domain: "example.test", ADKIM: "r", ASPF: "s", P: "quarantine", SP: "reject", Pct: "100",
		Records: []Record{
			{
				SourceIP: "198.51.100.7", Count: 3, Disposition: "none", DKIM: "pass", SPF: "fail",
				HeaderFrom: "example.test", EnvelopeFrom: "bounce.example.test",
				DKIMResults: []AuthResult{{Domain: "example.test", Result: "pass"}, {Domain: "other.test", Result: "fail"}},
				SPFResults:  []AuthResult{{Domain: "bounce.example.test", Result: "softfail"}},
			},
			{
				SourceIP: "2001:db8::1", Count: 1, Disposition: "quarantine", DKIM: "fail", SPF: "fail",
				HeaderFrom: "example.test", DKIMResults: []AuthResult{},
				SPFResults: []AuthResult{{Domain: "example.test", Result: "none"}},
			},
		},
	}
	doc, err := in.XML()
	if err != nil {
		t.Fatalf("XML: %v", err)
	}
	if !strings.HasPrefix(string(doc), "<?xml") || !strings.Contains(string(doc), "<feedback>") {
		t.Fatalf("document lacks the declaration or the feedback root:\n%s", doc)
	}
	if strings.Contains(string(doc), "<envelope_from></envelope_from>") || strings.Contains(string(doc), "<selector>") {
		t.Errorf("an empty optional element was written:\n%s", doc)
	}
	out, err := parseAggregate(doc)
	if err != nil {
		t.Fatalf("parse back: %v\n%s", err, doc)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip changed the report:\n in: %+v\nout: %+v", in, out)
	}
}

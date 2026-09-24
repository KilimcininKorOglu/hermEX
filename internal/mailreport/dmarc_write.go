package mailreport

import (
	"bytes"
	"encoding/xml"
)

// The output types mirror the aggregate report schema (RFC 7489 Appendix C) in
// element order. They are separate from the decode types so an optional element
// this server has no value for is left out rather than written empty, and so the
// parser stays tolerant of what other reporters send.
type xmlFeedbackOut struct {
	XMLName  xml.Name       `xml:"feedback"`
	Version  string         `xml:"version"`
	Metadata xmlMetadataOut `xml:"report_metadata"`
	Policy   xmlPolicyOut   `xml:"policy_published"`
	Records  []xmlRecordOut `xml:"record"`
}

type xmlMetadataOut struct {
	OrgName   string `xml:"org_name"`
	Email     string `xml:"email"`
	ReportID  string `xml:"report_id"`
	DateRange struct {
		Begin int64 `xml:"begin"`
		End   int64 `xml:"end"`
	} `xml:"date_range"`
}

type xmlPolicyOut struct {
	Domain string `xml:"domain"`
	ADKIM  string `xml:"adkim,omitempty"`
	ASPF   string `xml:"aspf,omitempty"`
	P      string `xml:"p"`
	SP     string `xml:"sp,omitempty"`
	Pct    string `xml:"pct,omitempty"`
}

type xmlRecordOut struct {
	Row struct {
		SourceIP string `xml:"source_ip"`
		Count    int64  `xml:"count"`
		Eval     struct {
			Disposition string `xml:"disposition"`
			DKIM        string `xml:"dkim"`
			SPF         string `xml:"spf"`
		} `xml:"policy_evaluated"`
	} `xml:"row"`
	Identifiers struct {
		EnvelopeFrom string `xml:"envelope_from,omitempty"`
		HeaderFrom   string `xml:"header_from"`
	} `xml:"identifiers"`
	Auth struct {
		DKIM []xmlAuthOut `xml:"dkim"`
		SPF  []xmlAuthOut `xml:"spf"`
	} `xml:"auth_results"`
}

type xmlAuthOut struct {
	Domain   string `xml:"domain"`
	Selector string `xml:"selector,omitempty"`
	Result   string `xml:"result"`
}

// XML renders the report as the aggregate report document a receiver expects
// (RFC 7489 §7.2.1.1): an XML declaration and a <feedback> root. The parser this
// package applies to received reports reads the result back to the same report.
func (a *Aggregate) XML() ([]byte, error) {
	out := xmlFeedbackOut{Version: "1.0"}
	out.Metadata.OrgName = a.OrgName
	out.Metadata.Email = a.Email
	out.Metadata.ReportID = a.ReportID
	out.Metadata.DateRange.Begin = a.Begin.Unix()
	out.Metadata.DateRange.End = a.End.Unix()
	out.Policy = xmlPolicyOut{Domain: a.Domain, ADKIM: a.ADKIM, ASPF: a.ASPF, P: a.P, SP: a.SP, Pct: a.Pct}
	out.Records = make([]xmlRecordOut, 0, len(a.Records))
	for _, r := range a.Records {
		out.Records = append(out.Records, recordOut(r))
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// recordOut converts one report row to its output form.
func recordOut(r Record) xmlRecordOut {
	var x xmlRecordOut
	x.Row.SourceIP = r.SourceIP
	x.Row.Count = r.Count
	x.Row.Eval.Disposition = r.Disposition
	x.Row.Eval.DKIM = r.DKIM
	x.Row.Eval.SPF = r.SPF
	x.Identifiers.EnvelopeFrom = r.EnvelopeFrom
	x.Identifiers.HeaderFrom = r.HeaderFrom
	x.Auth.DKIM = authOut(r.DKIMResults)
	x.Auth.SPF = authOut(r.SPFResults)
	return x
}

func authOut(in []AuthResult) []xmlAuthOut {
	out := make([]xmlAuthOut, 0, len(in))
	for _, a := range in {
		out = append(out, xmlAuthOut(a))
	}
	return out
}

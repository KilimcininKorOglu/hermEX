package mailreport

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/netip"
	"time"

	"hermex/internal/mime"
)

// Aggregate is one DMARC aggregate report (RFC 7489 §7.2, Appendix C): the
// reporter's identity, the policy it found published, and one record per source
// address and identifier set it saw.
type Aggregate struct {
	OrgName  string
	Email    string
	ReportID string
	Begin    time.Time
	End      time.Time

	// The published policy the reporter evaluated against. Pct is kept as sent,
	// because the updated DMARC specification drops the tag and an empty value
	// must stay distinguishable from 0.
	Domain string
	ADKIM  string
	ASPF   string
	P      string
	SP     string
	Pct    string

	Records []Record
}

// Record is one row of an aggregate report: how many messages one source sent
// under one set of identifiers, and how they were evaluated.
type Record struct {
	SourceIP     string
	Count        int64
	Disposition  string
	DKIM         string // the policy-evaluated DKIM verdict, "pass" or "fail"
	SPF          string // the policy-evaluated SPF verdict, "pass" or "fail"
	HeaderFrom   string
	EnvelopeFrom string
	DKIMResults  []AuthResult
	SPFResults   []AuthResult
}

// AuthResult is one raw authentication result behind a record's verdict.
// Selector is set for DKIM only.
type AuthResult struct {
	Domain   string
	Selector string
	Result   string
}

// xmlFeedback mirrors the aggregate report schema. The tags carry no namespace,
// so both the original schema and the namespaced one of the updated
// specification decode.
type xmlFeedback struct {
	XMLName  xml.Name
	Metadata struct {
		OrgName   string `xml:"org_name"`
		Email     string `xml:"email"`
		ReportID  string `xml:"report_id"`
		DateRange struct {
			Begin int64 `xml:"begin"`
			End   int64 `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	Policy struct {
		Domain string `xml:"domain"`
		ADKIM  string `xml:"adkim"`
		ASPF   string `xml:"aspf"`
		P      string `xml:"p"`
		SP     string `xml:"sp"`
		Pct    string `xml:"pct"`
	} `xml:"policy_published"`
	Records []xmlRecord `xml:"record"`
}

type xmlRecord struct {
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
		HeaderFrom   string `xml:"header_from"`
		EnvelopeFrom string `xml:"envelope_from"`
	} `xml:"identifiers"`
	Auth struct {
		DKIM []xmlAuth `xml:"dkim"`
		SPF  []xmlAuth `xml:"spf"`
	} `xml:"auth_results"`
}

type xmlAuth struct {
	Domain   string `xml:"domain"`
	Selector string `xml:"selector"`
	Result   string `xml:"result"`
}

// parseAggregatePart reads an aggregate report from one attachment.
func parseAggregatePart(p *mime.Part) (Result, error) {
	doc, err := aggregateDocument(p)
	if err != nil {
		return Result{}, err
	}
	agg, err := parseAggregate(doc)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: KindDMARCAggregate, Aggregate: agg}, nil
}

// aggregateDocument returns the attachment's XML document, decompressed.
func aggregateDocument(p *mime.Part) ([]byte, error) {
	data, err := p.DecodedContent()
	if err != nil {
		return nil, err
	}
	switch classify(p) {
	case formatZip:
		return unzipXML(data)
	case formatGzip:
		return gunzip(data)
	}
	if len(data) > maxReportBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

// parseAggregate decodes an aggregate report document. An XML document whose root
// is not <feedback> is some other attachment, so it reads as ErrNotReport.
func parseAggregate(doc []byte) (*Aggregate, error) {
	var fb xmlFeedback
	dec := xml.NewDecoder(bytes.NewReader(doc))
	dec.CharsetReader = charsetReader
	if err := dec.Decode(&fb); err != nil {
		return nil, fmt.Errorf("mailreport: aggregate report XML: %w", err)
	}
	if fb.XMLName.Local != "feedback" {
		return nil, ErrNotReport
	}
	if len(fb.Records) > maxRecords {
		return nil, ErrTooManyRecords
	}
	agg := &Aggregate{
		OrgName:  fb.Metadata.OrgName,
		Email:    fb.Metadata.Email,
		ReportID: fb.Metadata.ReportID,
		Begin:    time.Unix(fb.Metadata.DateRange.Begin, 0).UTC(),
		End:      time.Unix(fb.Metadata.DateRange.End, 0).UTC(),
		Domain:   fb.Policy.Domain,
		ADKIM:    fb.Policy.ADKIM,
		ASPF:     fb.Policy.ASPF,
		P:        fb.Policy.P,
		SP:       fb.Policy.SP,
		Pct:      fb.Policy.Pct,
		Records:  make([]Record, 0, len(fb.Records)),
	}
	for _, r := range fb.Records {
		rec, err := r.record()
		if err != nil {
			return nil, err
		}
		agg.Records = append(agg.Records, rec)
	}
	return agg, nil
}

// record converts one decoded row, refusing one no reporter would send: a source
// that is not an IP address or a negative message count.
func (r xmlRecord) record() (Record, error) {
	addr, err := netip.ParseAddr(r.Row.SourceIP)
	if err != nil {
		return Record{}, fmt.Errorf("mailreport: aggregate record source %q: %w", r.Row.SourceIP, err)
	}
	if r.Row.Count < 0 {
		return Record{}, fmt.Errorf("mailreport: aggregate record count %d is negative", r.Row.Count)
	}
	return Record{
		SourceIP:     addr.String(),
		Count:        r.Row.Count,
		Disposition:  r.Row.Eval.Disposition,
		DKIM:         r.Row.Eval.DKIM,
		SPF:          r.Row.Eval.SPF,
		HeaderFrom:   r.Identifiers.HeaderFrom,
		EnvelopeFrom: r.Identifiers.EnvelopeFrom,
		DKIMResults:  authResults(r.Auth.DKIM),
		SPFResults:   authResults(r.Auth.SPF),
	}, nil
}

func authResults(in []xmlAuth) []AuthResult {
	out := make([]AuthResult, 0, len(in))
	for _, a := range in {
		out = append(out, AuthResult(a))
	}
	return out
}

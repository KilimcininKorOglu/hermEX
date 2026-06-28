// Package tlsrpt implements SMTP TLS Reporting (RFC 8460): it discovers a
// recipient domain's reporting policy from its _smtp._tls TXT record, models the
// daily aggregate report, and classifies an outbound TLS outcome into one of the
// registry result types. It is the report-side counterpart to the mtasts and
// dane packages: those enforce TLS on delivery, this records and reports how that
// enforcement fared.
//
// The package is deliberately transport-free. It parses the policy (so a caller
// knows where a domain wants its reports sent) and produces the report JSON; it
// does not itself deliver reports over HTTPS or mail.
package tlsrpt

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// version is the required first field of a TLS-RPT TXT record (RFC 8460 §3).
const version = "v=TLSRPTv1"

// Policy is a domain's published TLS-RPT reporting policy: the report URIs
// (rua=) a sender should deliver aggregate reports to. Each URI is a "mailto:"
// or "https:" destination (RFC 8460 §3); other schemes are rejected at parse.
type Policy struct {
	// RUAs are the aggregate-report URIs in published order, at least one.
	RUAs []string
}

// Parse reads a TLS-RPT TXT record (RFC 8460 §3). The record is a sequence of
// ";"-delimited "key=value" fields; the first MUST be "v=TLSRPTv1" and a "rua="
// field carrying one or more comma-separated URIs is required. Unknown extension
// fields are ignored so a record may carry future fields without failing.
func Parse(txt string) (*Policy, error) {
	fields := strings.Split(txt, ";")
	// The version field is required and must come first (RFC 8460 §3).
	if strings.TrimSpace(fields[0]) != version {
		return nil, fmt.Errorf("tlsrpt: record does not start with %q", version)
	}
	var ruas []string
	for _, f := range fields[1:] {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		key, val, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("tlsrpt: malformed field %q", f)
		}
		// Only rua is interpreted; any other field is a tolerated extension.
		if strings.TrimSpace(key) != "rua" {
			continue
		}
		for uri := range strings.SplitSeq(val, ",") {
			uri = strings.TrimSpace(uri)
			if !validURI(uri) {
				return nil, fmt.Errorf("tlsrpt: unsupported report URI %q", uri)
			}
			ruas = append(ruas, uri)
		}
	}
	if len(ruas) == 0 {
		return nil, errors.New("tlsrpt: record has no usable rua= report URI")
	}
	return &Policy{RUAs: ruas}, nil
}

// validURI reports whether a rua target is a scheme this package can deliver to:
// "mailto:" or "https:" (RFC 8460 §3 defines these two; an http: target is not
// permitted for report delivery).
func validURI(uri string) bool {
	return strings.HasPrefix(uri, "mailto:") || strings.HasPrefix(uri, "https:")
}

// Resolver discovers a domain's TLS-RPT policy from DNS. LookupTXT is injectable
// so tests can supply records without a network; nil uses net.LookupTXT.
type Resolver struct {
	LookupTXT func(name string) ([]string, error)
}

// Lookup fetches and parses the TLS-RPT policy published at _smtp._tls.<domain>
// (RFC 8460 §3). It returns (nil, nil) when the domain publishes no TLS-RPT
// record, so the caller can treat "no policy" distinctly from a malformed one.
// A record present but unparseable is an error.
func (r *Resolver) Lookup(domain string) (*Policy, error) {
	lookup := r.LookupTXT
	if lookup == nil {
		lookup = net.LookupTXT
	}
	records, err := lookup("_smtp._tls." + domain)
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		if strings.HasPrefix(strings.TrimSpace(rec), version) {
			return Parse(rec)
		}
	}
	return nil, nil
}

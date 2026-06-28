package tlsrpt

import (
	"errors"
	"reflect"
	"testing"
)

// TestParseValid proves the version-first, rua-required record (RFC 8460 §3)
// parses into its report URIs, tolerating whitespace and ignoring unknown
// extension fields so a future field does not break an existing record.
func TestParseValid(t *testing.T) {
	cases := []struct {
		name string
		txt  string
		want []string
	}{
		{"mailto", "v=TLSRPTv1;rua=mailto:reports@example.com", []string{"mailto:reports@example.com"}},
		{"https", "v=TLSRPTv1; rua=https://reporting.example.com/v1/tlsrpt", []string{"https://reporting.example.com/v1/tlsrpt"}},
		{"two URIs", "v=TLSRPTv1; rua=mailto:a@example.com,https://r.example.com/x", []string{"mailto:a@example.com", "https://r.example.com/x"}},
		{"extension ignored", "v=TLSRPTv1; rua=mailto:a@example.com; ext=value", []string{"mailto:a@example.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := Parse(c.txt)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", c.txt, err)
			}
			if !reflect.DeepEqual(p.RUAs, c.want) {
				t.Errorf("RUAs = %v, want %v", p.RUAs, c.want)
			}
		})
	}
}

// TestParseRejects proves a record missing the version prefix, missing a usable
// rua, or naming an unsupported scheme is an error rather than a silent empty
// policy, so a sender never treats a broken record as "report nowhere".
func TestParseRejects(t *testing.T) {
	cases := []struct {
		name string
		txt  string
	}{
		{"no version", "rua=mailto:a@example.com"},
		{"wrong version", "v=TLSRPTv2; rua=mailto:a@example.com"},
		{"no rua", "v=TLSRPTv1; ext=value"},
		{"empty rua", "v=TLSRPTv1; rua="},
		{"unsupported scheme", "v=TLSRPTv1; rua=ftp://reports.example.com"},
		{"http not https", "v=TLSRPTv1; rua=http://reports.example.com"},
		{"malformed field", "v=TLSRPTv1; rua"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(c.txt); err == nil {
				t.Errorf("Parse(%q) should have failed", c.txt)
			}
		})
	}
}

// TestResolverLookup proves Lookup queries the _smtp._tls name, returns the
// parsed policy when a TLSRPTv1 record is present, and returns (nil, nil) for a
// domain with no TLS-RPT record so "no policy" is distinct from a parse error.
func TestResolverLookup(t *testing.T) {
	var asked string
	r := &Resolver{LookupTXT: func(name string) ([]string, error) {
		asked = name
		switch name {
		case "_smtp._tls.has.example":
			return []string{"v=spf1 -all", "v=TLSRPTv1; rua=mailto:r@has.example"}, nil
		case "_smtp._tls.none.example":
			return []string{"v=spf1 -all"}, nil
		}
		return nil, errors.New("unexpected name " + name)
	}}

	p, err := r.Lookup("has.example")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	if asked != "_smtp._tls.has.example" {
		t.Errorf("queried %q, want _smtp._tls.has.example", asked)
	}
	if p == nil || len(p.RUAs) != 1 || p.RUAs[0] != "mailto:r@has.example" {
		t.Errorf("policy = %+v, want one mailto rua", p)
	}

	p, err = r.Lookup("none.example")
	if err != nil {
		t.Fatalf("Lookup(none) error: %v", err)
	}
	if p != nil {
		t.Errorf("a domain with no TLS-RPT record must return nil policy, got %+v", p)
	}
}

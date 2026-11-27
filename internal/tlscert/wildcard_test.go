package tlscert

import (
	"crypto/tls"
	"strings"
	"testing"
)

// TestWildcardNameServesOneLabel stores a certificate under a wildcard name. RFC
// 6125 6.4.3 lets "*.example.com" stand for exactly one leftmost label, so it must
// serve mail.example.com, while a deeper name and the bare parent fall through to
// the default, and an exact name still wins over the wildcard.
func TestWildcardNameServesOneLabel(t *testing.T) {
	def, wild, exact := &tls.Certificate{}, &tls.Certificate{}, &tls.Certificate{}
	p := &Provider{}
	p.snap.Store(&snapshot{byName: map[string]*tls.Certificate{
		"": def, "*.example.com": wild, "webmail.example.com": exact,
	}})

	cases := []struct {
		sni  string
		want *tls.Certificate
		what string
	}{
		{"mail.example.com", wild, "a name one label under the wildcard"},
		{"MAIL.Example.COM", wild, "the same name in another case"},
		{"webmail.example.com", exact, "a name stored exactly, over the wildcard"},
		{"a.b.example.com", def, "a name two labels under the wildcard"},
		{"example.com", def, "the wildcard's bare parent"},
	}
	for _, c := range cases {
		got, err := p.getCertificate(helloFor(c.sni))
		if err != nil {
			t.Errorf("%s (%q) returned unexpected error: %v", c.what, c.sni, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s (%q) served the wrong certificate", c.what, c.sni)
		}
	}
}

// TestWildcardRefusesHostileAndUnmatchedSNI proves that without a default or file
// fallback, wildcard matching refuses single-label TLD wildcards (RFC 6125 6.4.3),
// multi-label depths, unrelated hosts, empty SNI, and oversized labels without
// altering the provider snapshot.
func TestWildcardRefusesHostileAndUnmatchedSNI(t *testing.T) {
	wild, tldWild := &tls.Certificate{}, &tls.Certificate{}
	p := &Provider{}
	p.snap.Store(&snapshot{byName: map[string]*tls.Certificate{
		"*.example.com": wild,
		"*.com":         tldWild,
	}})

	refused := []string{
		"example.com",
		"a.b.example.com",
		"evil.other.net",
		strings.Repeat("a", 300) + ".other.net",
		"",
	}
	for _, sni := range refused {
		got, err := p.getCertificate(helloFor(sni))
		if err == nil || got != nil {
			t.Errorf("SNI %q must be refused, got (%v, %v)", sni, got, err)
		}
	}
	if len(p.snap.Load().byName) != 2 {
		t.Errorf("refusal altered snapshot len to %d, want 2", len(p.snap.Load().byName))
	}
	if got, err := p.getCertificate(helloFor("mail.example.com")); err != nil || got != wild {
		t.Errorf("valid wildcard SNI after refusals returned (%v, %v), want wild cert", got, err)
	}
}

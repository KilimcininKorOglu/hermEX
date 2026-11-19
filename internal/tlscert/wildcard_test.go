package tlscert

import (
	"crypto/tls"
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
		if got, _ := p.getCertificate(helloFor(c.sni)); got != c.want {
			t.Errorf("%s (%q) served the wrong certificate", c.what, c.sni)
		}
	}
}

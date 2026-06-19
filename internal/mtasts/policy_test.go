package mtasts

import "testing"

// TestParsePolicy proves a well-formed policy parses and every missing or invalid
// required field is rejected, so the relay never enforces against a half-parsed
// policy. A withdrawing policy (mode none) is allowed without mx.
func TestParsePolicy(t *testing.T) {
	p, err := Parse("version: STSv1\r\nmode: enforce\r\nmx: mail.example.com\r\nmx: *.example.net\r\nmax_age: 604800\r\n")
	if err != nil {
		t.Fatalf("Parse(valid): %v", err)
	}
	if p.Mode != ModeEnforce {
		t.Errorf("Mode = %q, want enforce", p.Mode)
	}
	if len(p.MX) != 2 || p.MX[0] != "mail.example.com" || p.MX[1] != "*.example.net" {
		t.Errorf("MX = %v", p.MX)
	}
	if p.MaxAge != 604800 {
		t.Errorf("MaxAge = %d", p.MaxAge)
	}

	for name, doc := range map[string]string{
		"no version":   "mode: enforce\nmx: a.example\nmax_age: 1\n",
		"bad version":  "version: STSv2\nmode: enforce\nmx: a.example\nmax_age: 1\n",
		"unknown mode": "version: STSv1\nmode: sometimes\nmx: a.example\nmax_age: 1\n",
		"no mx":        "version: STSv1\nmode: enforce\nmax_age: 1\n",
		"zero max_age": "version: STSv1\nmode: enforce\nmx: a.example\nmax_age: 0\n",
	} {
		if _, err := Parse(doc); err == nil {
			t.Errorf("Parse(%s) should fail", name)
		}
	}

	if _, err := Parse("version: STSv1\nmode: none\nmax_age: 1\n"); err != nil {
		t.Errorf("mode none should parse without mx: %v", err)
	}
}

// TestMatchesMX proves the wildcard matches exactly one label, the match is
// case-insensitive, and a trailing root dot is ignored.
func TestMatchesMX(t *testing.T) {
	p := &Policy{MX: []string{"mail.example.com", "*.example.net"}}
	for host, want := range map[string]bool{
		"mail.example.com":  true,  // exact
		"MAIL.EXAMPLE.COM":  true,  // case-insensitive
		"mail.example.com.": true,  // trailing root dot
		"foo.example.net":   true,  // one-label wildcard
		"a.b.example.net":   false, // wildcard is one label only
		"example.net":       false, // wildcard needs a label
		"other.example.org": false, // matches no pattern
	} {
		if got := p.MatchesMX(host); got != want {
			t.Errorf("MatchesMX(%q) = %v, want %v", host, got, want)
		}
	}
}

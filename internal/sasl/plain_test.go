package sasl

import "testing"

// TestParsePlain takes its cases from RFC 4616 section 2: the message is
// [authzid] NUL authcid NUL passwd, where authcid and passwd are non-empty and no
// field contains NUL, and the server fails the exchange when the authentication
// identity may not act as the authorization identity. This server grants no
// proxy rights, so an authzid must be empty or name the authcid itself.
func TestParsePlain(t *testing.T) {
	good := []struct{ in, user, pass string }{
		{"\x00alice\x00secret", "alice", "secret"},
		{"alice\x00alice\x00secret", "alice", "secret"},
		{"Alice@Example.com\x00alice@example.com\x00pw", "alice@example.com", "pw"},
	}
	for _, c := range good {
		user, pass, ok := ParsePlain(c.in)
		if !ok || user != c.user || pass != c.pass {
			t.Errorf("ParsePlain(%q) = %q, %q, %v; want %q, %q, true", c.in, user, pass, ok, c.user, c.pass)
		}
	}
	for _, bad := range []string{
		"bob\x00alice\x00secret",  // acting as another identity
		"alice\x00secret",         // two fields
		"\x00alice\x00sec\x00ret", // a NUL inside the password
		"\x00\x00secret",          // empty authcid
		"\x00alice\x00",           // empty password
		"",                        // nothing at all
	} {
		if user, _, ok := ParsePlain(bad); ok {
			t.Errorf("ParsePlain(%q) accepted user %q, want a refusal", bad, user)
		}
	}
}

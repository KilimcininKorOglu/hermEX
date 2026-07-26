package serve

import "testing"

func TestValidPublicHost(t *testing.T) {
	valid := []string{
		"mail.example.com",
		"mail.example.com:443",
		"a.b.c.example.org",
		"mail.example.com.", // trailing dot is a valid absolute FQDN
	}
	for _, h := range valid {
		if !ValidPublicHost(h) {
			t.Errorf("ValidPublicHost(%q) = false, want true", h)
		}
	}
	invalid := []string{
		"",
		"localhost",
		"intranet",         // single label
		"127.0.0.1",        // loopback IP literal
		"10.0.0.1:8080",    // private IP literal with port
		"192.168.1.5",      // private IP literal
		"::1",              // IPv6 loopback
		"[::1]:443",        // bracketed IPv6 with port
		"evil.com/../path", // separator in label
		"a_b.example.com",  // underscore not allowed
	}
	for _, h := range invalid {
		if ValidPublicHost(h) {
			t.Errorf("ValidPublicHost(%q) = true, want false", h)
		}
	}
}

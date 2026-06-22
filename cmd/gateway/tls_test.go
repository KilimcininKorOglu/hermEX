package main

import (
	"strings"
	"testing"
)

// TestExpandACMENames proves the certificate allowlist covers the mail/autodiscover/
// autoconfig hosts a client is steered to per domain plus the server hostname,
// deduplicated, and that the apex is excluded — it is the tenant's own website and
// would not resolve to the gateway. Coverage matters because a missing name means
// that host presents no valid certificate; the mail hosts in particular never reach
// the gateway over HTTP, so on-demand issuance would skip them and only this
// proactive set saves them.
func TestExpandACMENames(t *testing.T) {
	got := expandACMENames([]string{"tenant.com", "other.org"}, "mail.hermex.test")
	want := []string{
		"autoconfig.other.org", "autoconfig.tenant.com",
		"autodiscover.other.org", "autodiscover.tenant.com",
		"mail.hermex.test",
		"mail.other.org", "mail.tenant.com",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("expandACMENames =\n  %v\nwant (sorted, deduped, no apex)\n  %v", got, want)
	}
	for _, n := range got {
		if n == "tenant.com" || n == "other.org" {
			t.Errorf("apex %q must not be in the allowlist", n)
		}
	}
}

// TestExpandACMENamesDedupesHostname proves the server hostname is not duplicated when
// it is also one of a domain's derived hosts (hostname mail.tenant.com vs the domain
// tenant.com's mail. host), so the obtain list carries each name exactly once.
func TestExpandACMENamesDedupesHostname(t *testing.T) {
	got := expandACMENames([]string{"tenant.com"}, "mail.tenant.com")
	want := []string{"autoconfig.tenant.com", "autodiscover.tenant.com", "mail.tenant.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("expandACMENames = %v, want %v with mail.tenant.com appearing once", got, want)
	}
}

// TestExpandACMENamesEmpty proves no domains and no hostname yield an empty list
// rather than a slice with empty strings — CertMagic must not be asked to manage "".
func TestExpandACMENamesEmpty(t *testing.T) {
	if got := expandACMENames([]string{""}, ""); len(got) != 0 {
		t.Errorf("expandACMENames(empty) = %v, want no names", got)
	}
}

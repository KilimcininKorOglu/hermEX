package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// TestExpandACMENames proves the certificate allowlist covers the mail/autodiscover/
// autoconfig hosts a client is steered to per domain plus the server hostname,
// deduplicated, and that the apex is excluded, it is the tenant's own website and
// would not resolve to the gateway. Coverage matters because a missing name means
// that host presents no valid certificate; the mail hosts in particular never reach
// the gateway over HTTP, so on-demand issuance would skip them and only this
// proactive set saves them.
func TestExpandACMENames(t *testing.T) {
	got := expandACMENames([]string{"tenant.com", "other.org"}, "mail.hermex.test", false)
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
		if strings.HasPrefix(n, "mta-sts.") {
			t.Errorf("mta-sts host %q must not appear when MTA-STS is disabled", n)
		}
	}
}

// TestExpandACMENamesMTASTS proves the mta-sts.<domain> policy host enters the
// allowlist only when MTA-STS publishing is enabled, obtaining a certificate for it
// before the owner has pointed the host at the gateway would fail TLS-ALPN-01 and
// burn the CA rate limit, so it must not be requested speculatively.
func TestExpandACMENamesMTASTS(t *testing.T) {
	on := expandACMENames([]string{"tenant.com"}, "mail.hermex.test", true)
	want := []string{
		"autoconfig.tenant.com", "autodiscover.tenant.com",
		"mail.hermex.test", "mail.tenant.com", "mta-sts.tenant.com",
	}
	if strings.Join(on, ",") != strings.Join(want, ",") {
		t.Errorf("expandACMENames(enabled) =\n  %v\nwant\n  %v", on, want)
	}
	off := expandACMENames([]string{"tenant.com"}, "mail.hermex.test", false)
	for _, n := range off {
		if n == "mta-sts.tenant.com" {
			t.Error("mta-sts.tenant.com present with MTA-STS disabled")
		}
	}
}

// TestACMENamesRecordAnUnreadableMTASTSSetting proves an MTA-STS settings read
// failure keeps the mail, autodiscover and autoconfig certificates (a client needs
// them whatever the MTA-STS state is), leaves out the mta-sts host (RFC 8461 3.3:
// publishing a policy host is the operator's choice, and it cannot be known), and
// records the failure, so the missing mta-sts certificate has a cause in the log.
func TestACMENamesRecordAnUnreadableMTASTSSetting(t *testing.T) {
	sink := &eventSink{}
	dir := &fakeMTASTSDir{
		domains:     []directory.DomainInfo{{Name: "tenant.com"}},
		settingsErr: errors.New("db down: settings"),
	}

	names, err := acmeNames(dir, "mail.hermex.test", "", logging.New(sink))
	if err != nil {
		t.Fatalf("acmeNames: %v", err)
	}
	if !slices.Contains(names, "mail.tenant.com") || slices.Contains(names, "mta-sts.tenant.com") {
		t.Errorf("names = %v, want the mail host and no mta-sts host", names)
	}
	recorded := false
	for _, e := range sink.events {
		if e.Name == "settings.read" && e.Fields["error"] == "db down: settings" {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("events = %+v, want a settings.read event naming the error", sink.events)
	}
}

// TestExpandACMENamesDedupesHostname proves the server hostname is not duplicated when
// it is also one of a domain's derived hosts (hostname mail.tenant.com vs the domain
// tenant.com's mail. host), so the obtain list carries each name exactly once.
func TestExpandACMENamesDedupesHostname(t *testing.T) {
	got := expandACMENames([]string{"tenant.com"}, "mail.tenant.com", false)
	want := []string{"autoconfig.tenant.com", "autodiscover.tenant.com", "mail.tenant.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("expandACMENames = %v, want %v with mail.tenant.com appearing once", got, want)
	}
}

// TestExpandACMENamesIgnoresCase proves names that differ only in case are one
// name: DNS names compare case-insensitively (RFC 4343), so an operator who types
// the hostname as Mail.Tenant.com in config.json must not get two certificate
// orders for the same host.
func TestExpandACMENamesIgnoresCase(t *testing.T) {
	got := expandACMENames([]string{"Tenant.COM"}, "Mail.Tenant.com", false)
	want := []string{"autoconfig.tenant.com", "autodiscover.tenant.com", "mail.tenant.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("expandACMENames = %v, want %v", got, want)
	}
}

// TestExpandACMENamesAddsTheAdminHost: the admin panel's host name is served by
// the gateway, so it needs a certificate like the mail hosts. It is compared the
// way DNS compares names (RFC 4343, root dot dropped), and an unset one adds none.
func TestExpandACMENamesAddsTheAdminHost(t *testing.T) {
	got := expandACMENames([]string{"tenant.com"}, "mail.tenant.com", false, "Admin.Tenant.com.")
	want := []string{"admin.tenant.com", "autoconfig.tenant.com", "autodiscover.tenant.com", "mail.tenant.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("expandACMENames = %v, want %v", got, want)
	}
	if got := expandACMENames([]string{"tenant.com"}, "mail.tenant.com", false, ""); len(got) != 3 {
		t.Errorf("an unset admin host added a name: %v", got)
	}
}

// TestExpandACMENamesEmpty proves no domains and no hostname yield an empty list
// rather than a slice with empty strings, CertMagic must not be asked to manage "".
func TestExpandACMENamesEmpty(t *testing.T) {
	if got := expandACMENames([]string{""}, "", false); len(got) != 0 {
		t.Errorf("expandACMENames(empty) = %v, want no names", got)
	}
}

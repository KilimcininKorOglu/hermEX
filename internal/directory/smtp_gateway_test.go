package directory

import (
	"strings"
	"testing"
)

// aGateway is a usable gateway configuration the tests vary one field of.
func aGateway() SMTPGateway {
	return SMTPGateway{
		Enabled: true, Host: "smtp.provider.example", Port: 587,
		Encryption: GatewaySTARTTLS, Username: "relay", Password: "s3cret",
	}
}

// mustSetGateway stores a gateway and requires it to be accepted.
func mustSetGateway(t *testing.T, d *SQLDirectory, domain string, g SMTPGateway) {
	t.Helper()
	mustNoErr(t, "store the gateway for "+domain, d.SetSMTPGateway(domain, g))
}

// TestGatewayRoundTrips proves a stored global gateway reads back with its password, which
// is what the MTA needs to authenticate.
func TestGatewayRoundTrips(t *testing.T) {
	d := freshSQL(t)
	mustSetGateway(t, d, GlobalGateway, aGateway())

	got, found, err := d.GetSMTPGateway(GlobalGateway)
	mustNoErr(t, "read the gateway", err)

	wantEq(t, "the gateway was found", found, true)
	wantEq(t, "the stored password", got.Password, "s3cret")
}

// TestGatewayStoresItsHostAndPort proves the connection details survive the round trip.
func TestGatewayStoresItsHostAndPort(t *testing.T) {
	d := freshSQL(t)
	mustSetGateway(t, d, GlobalGateway, aGateway())

	got, found, err := d.GetSMTPGateway(GlobalGateway)
	mustNoErr(t, "read the gateway", err)
	wantEq(t, "the gateway was found", found, true)

	wantEq(t, "the stored host", got.Host, "smtp.provider.example")
	wantEq(t, "the stored port", got.Port, 587)
}

// TestGatewayMissingIsNotFound proves an unconfigured key reports not-found rather than an
// empty gateway that would look configured.
func TestGatewayMissingIsNotFound(t *testing.T) {
	d := freshSQL(t)

	empty, found, err := d.GetSMTPGateway(GlobalGateway)
	mustNoErr(t, "read a missing gateway", err)

	wantEq(t, "an unconfigured gateway is not found", found, false)
	wantEq(t, "missing gateway zero value", empty, SMTPGateway{})
}

// TestGatewayPasswordIsEncryptedAtRest is the load-bearing security case: a database dump
// must not carry the AUTH password in usable form.
func TestGatewayPasswordIsEncryptedAtRest(t *testing.T) {
	d := freshSQL(t)
	d.SetKeySecret([]byte("test-key-secret"))
	mustSetGateway(t, d, GlobalGateway, aGateway())

	var stored string
	if err := d.db.QueryRow(`SELECT password FROM smtp_gateways WHERE domain = ''`).Scan(&stored); err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(stored, wrapPrefix) || strings.Contains(stored, "s3cret") {
		t.Errorf("the gateway password is not stored wrapped: %q", stored)
	}
}

// TestGatewayPasswordUnwrapsForTheMTA proves the wrapped password is readable again, so
// encryption at rest does not break authentication.
func TestGatewayPasswordUnwrapsForTheMTA(t *testing.T) {
	d := freshSQL(t)
	d.SetKeySecret([]byte("test-key-secret"))
	mustSetGateway(t, d, GlobalGateway, aGateway())

	got, found, err := d.GetSMTPGateway(GlobalGateway)
	mustNoErr(t, "read the gateway", err)
	wantEq(t, "the gateway was found", found, true)

	wantEq(t, "the unwrapped password", got.Password, "s3cret")
}

// TestGatewayRejectsUnknownEncryption proves an unusable transport is refused on write, so
// the MTA never polls up a configuration it cannot dial.
func TestGatewayRejectsUnknownEncryption(t *testing.T) {
	d := freshSQL(t)
	g := aGateway()
	g.Encryption = "quantum"

	if err := d.SetSMTPGateway(GlobalGateway, g); err == nil {
		t.Error("an unknown encryption must be refused")
	}
}

// TestGatewayRejectsAnEnabledGatewayWithNoHost proves an enabled gateway must name a
// server, because every outbound delivery would otherwise fail.
func TestGatewayRejectsAnEnabledGatewayWithNoHost(t *testing.T) {
	d := freshSQL(t)
	g := aGateway()
	g.Host = ""

	if err := d.SetSMTPGateway(GlobalGateway, g); err == nil {
		t.Error("an enabled gateway with no host must be refused")
	}
}

// TestGatewayRejectsAPortOutOfRange proves a port that cannot be dialled is refused.
func TestGatewayRejectsAPortOutOfRange(t *testing.T) {
	d := freshSQL(t)
	g := aGateway()
	g.Port = 70000

	if err := d.SetSMTPGateway(GlobalGateway, g); err == nil {
		t.Error("a port outside 1-65535 must be refused")
	}
}

// TestListGatewaysKeysByDomain proves the poll's single read separates the global default
// from a domain's override.
func TestListGatewaysKeysByDomain(t *testing.T) {
	d := freshSQL(t)
	mustSetGateway(t, d, GlobalGateway, aGateway())
	tenant := aGateway()
	tenant.Host = "tenant-gw.example"
	mustSetGateway(t, d, "tenant.test", tenant)

	got, err := d.ListSMTPGateways()
	mustNoErr(t, "list the gateways", err)

	wantEq(t, "the global gateway's host", got[GlobalGateway].Host, "smtp.provider.example")
	wantEq(t, "the domain override's host", got["tenant.test"].Host, "tenant-gw.example")
}

// TestListGatewaysOmitsADisabledOne proves a configured but disabled gateway is not
// installed, so turning it off returns delivery to the direct path without losing settings.
func TestListGatewaysOmitsADisabledOne(t *testing.T) {
	d := freshSQL(t)
	g := aGateway()
	g.Enabled = false
	mustSetGateway(t, d, GlobalGateway, g)

	got, err := d.ListSMTPGateways()
	mustNoErr(t, "list the gateways", err)

	wantEq(t, "a disabled gateway is not listed", len(got), 0)
}

// TestGatewayLowercasesTheDomainKey proves a gateway saved under a mixed-case domain is
// found by the lower-cased sender domain the relay extracts.
func TestGatewayLowercasesTheDomainKey(t *testing.T) {
	d := freshSQL(t)
	mustSetGateway(t, d, "Tenant.Test", aGateway())

	got, found, err := d.GetSMTPGateway("tenant.test")
	mustNoErr(t, "read the gateway", err)

	wantEq(t, "the mixed-case domain was found lower-cased", found, true)
	wantEq(t, "lower-cased gateway host", got.Host, "smtp.provider.example")
}

// TestDeleteGatewayRemovesTheOverride proves removing a domain's row returns it to the
// global gateway.
func TestDeleteGatewayRemovesTheOverride(t *testing.T) {
	d := freshSQL(t)
	mustSetGateway(t, d, "tenant.test", aGateway())

	removed, err := d.DeleteSMTPGateway("tenant.test")
	mustNoErr(t, "delete the override", err)

	wantEq(t, "the delete removed the override", removed, true)
	empty, found, err := d.GetSMTPGateway("tenant.test")
	mustNoErr(t, "read the removed override", err)
	wantEq(t, "the override after the delete", found, false)
	wantEq(t, "deleted override zero value", empty, SMTPGateway{})
}

// TestDeleteGatewayLeavesTheGlobalOne proves removing a domain's override never
// takes the global gateway with it.
func TestDeleteGatewayLeavesTheGlobalOne(t *testing.T) {
	d := freshSQL(t)
	mustSetGateway(t, d, GlobalGateway, aGateway())
	mustSetGateway(t, d, "tenant.test", aGateway())
	removed, err := d.DeleteSMTPGateway("tenant.test")
	mustNoErr(t, "delete the override", err)
	wantEq(t, "override removed", removed, true)
	got, found, err := d.GetSMTPGateway(GlobalGateway)
	mustNoErr(t, "read the global gateway", err)
	wantEq(t, "the global gateway after the override delete", found, true)
	wantEq(t, "surviving global gateway host", got.Host, "smtp.provider.example")
}

// TestGatewayAcceptsAnIPHost proves an IP literal, the other usable host form, is
// still stored.
func TestGatewayAcceptsAnIPHost(t *testing.T) {
	d := freshSQL(t)
	for _, host := range []string{"192.0.2.25", "2001:db8::25"} {
		g := aGateway()
		g.Host = host
		mustSetGateway(t, d, GlobalGateway, g)
		got, found, err := d.GetSMTPGateway(GlobalGateway)
		mustNoErr(t, "read the gateway at "+host, err)
		wantEq(t, "gateway found at "+host, found, true)
		wantEq(t, "the stored IP host", got.Host, host)
	}
}

// TestGatewayRefusalKeepsTheStoredOne proves every refused write leaves the stored
// gateway as it was, so a typo in the form never breaks relaying.
func TestGatewayRefusalKeepsTheStoredOne(t *testing.T) {
	d := freshSQL(t)
	mustSetGateway(t, d, GlobalGateway, aGateway())
	for name, mutate := range map[string]func(*SMTPGateway){
		"unknown encryption":  func(g *SMTPGateway) { g.Encryption = "quantum" },
		"no host":             func(g *SMTPGateway) { g.Host = "" },
		"port 70000":          func(g *SMTPGateway) { g.Port = 70000 },
		"port 0":              func(g *SMTPGateway) { g.Port = 0 },
		"negative port":       func(g *SMTPGateway) { g.Port = -25 },
		"host with a newline": func(g *SMTPGateway) { g.Host = "smtp.example\r\nMAIL FROM:<x@evil.example>" },
		"host with a port":    func(g *SMTPGateway) { g.Host = "smtp.example:25" },
		"label with a hyphen": func(g *SMTPGateway) { g.Host = "-smtp.example" },
		"label with _":        func(g *SMTPGateway) { g.Host = "smtp_relay.example" },
	} {
		g := aGateway()
		g.Host = "changed.example"
		mutate(&g)
		if err := d.SetSMTPGateway(GlobalGateway, g); err == nil {
			t.Errorf("the gateway with %s was accepted", name)
		}
		got, found, err := d.GetSMTPGateway(GlobalGateway)
		mustNoErr(t, "read the gateway after the refusal", err)
		wantEq(t, "gateway found after the refused "+name, found, true)
		wantEq(t, "host after the refused "+name, got.Host, "smtp.provider.example")
	}
}

// TestDeleteGatewayReportsNothingRemoved proves deleting an absent gateway is not an error
// and reports honestly.
func TestDeleteGatewayReportsNothingRemoved(t *testing.T) {
	d := freshSQL(t)

	removed, err := d.DeleteSMTPGateway("tenant.test")
	mustNoErr(t, "delete an absent gateway", err)

	wantEq(t, "nothing was removed", removed, false)
}

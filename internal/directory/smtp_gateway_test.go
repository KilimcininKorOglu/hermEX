package directory

import "testing"

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
	d, _ := freshDirectory(t)
	mustSetGateway(t, d, GlobalGateway, aGateway())

	got, found, err := d.GetSMTPGateway(GlobalGateway)
	mustNoErr(t, "read the gateway", err)

	wantEq(t, "the gateway was found", found, true)
	wantEq(t, "the stored password", got.Password, "s3cret")
}

// TestGatewayStoresItsHostAndPort proves the connection details survive the round trip.
func TestGatewayStoresItsHostAndPort(t *testing.T) {
	d, _ := freshDirectory(t)
	mustSetGateway(t, d, GlobalGateway, aGateway())

	got, _, err := d.GetSMTPGateway(GlobalGateway)
	mustNoErr(t, "read the gateway", err)

	wantEq(t, "the stored host", got.Host, "smtp.provider.example")
	wantEq(t, "the stored port", got.Port, 587)
}

// TestGatewayMissingIsNotFound proves an unconfigured key reports not-found rather than an
// empty gateway that would look configured.
func TestGatewayMissingIsNotFound(t *testing.T) {
	d, _ := freshDirectory(t)

	_, found, err := d.GetSMTPGateway(GlobalGateway)
	mustNoErr(t, "read a missing gateway", err)

	wantEq(t, "an unconfigured gateway is not found", found, false)
}

// TestGatewayPasswordIsEncryptedAtRest is the load-bearing security case: a database dump
// must not carry the AUTH password in usable form.
func TestGatewayPasswordIsEncryptedAtRest(t *testing.T) {
	d, _ := freshDirectory(t)
	d.SetKeySecret([]byte("test-key-secret"))
	mustSetGateway(t, d, GlobalGateway, aGateway())

	var stored string
	if err := d.db.QueryRow(`SELECT password FROM smtp_gateways WHERE domain = ''`).Scan(&stored); err != nil {
		t.Fatal(err)
	}

	if stored == "s3cret" {
		t.Error("the gateway password is stored in plaintext")
	}
}

// TestGatewayPasswordUnwrapsForTheMTA proves the wrapped password is readable again, so
// encryption at rest does not break authentication.
func TestGatewayPasswordUnwrapsForTheMTA(t *testing.T) {
	d, _ := freshDirectory(t)
	d.SetKeySecret([]byte("test-key-secret"))
	mustSetGateway(t, d, GlobalGateway, aGateway())

	got, _, err := d.GetSMTPGateway(GlobalGateway)
	mustNoErr(t, "read the gateway", err)

	wantEq(t, "the unwrapped password", got.Password, "s3cret")
}

// TestGatewayRejectsUnknownEncryption proves an unusable transport is refused on write, so
// the MTA never polls up a configuration it cannot dial.
func TestGatewayRejectsUnknownEncryption(t *testing.T) {
	d, _ := freshDirectory(t)
	g := aGateway()
	g.Encryption = "quantum"

	if err := d.SetSMTPGateway(GlobalGateway, g); err == nil {
		t.Error("an unknown encryption must be refused")
	}
}

// TestGatewayRejectsAnEnabledGatewayWithNoHost proves an enabled gateway must name a
// server, because every outbound delivery would otherwise fail.
func TestGatewayRejectsAnEnabledGatewayWithNoHost(t *testing.T) {
	d, _ := freshDirectory(t)
	g := aGateway()
	g.Host = ""

	if err := d.SetSMTPGateway(GlobalGateway, g); err == nil {
		t.Error("an enabled gateway with no host must be refused")
	}
}

// TestGatewayRejectsAPortOutOfRange proves a port that cannot be dialled is refused.
func TestGatewayRejectsAPortOutOfRange(t *testing.T) {
	d, _ := freshDirectory(t)
	g := aGateway()
	g.Port = 70000

	if err := d.SetSMTPGateway(GlobalGateway, g); err == nil {
		t.Error("a port outside 1-65535 must be refused")
	}
}

// TestListGatewaysKeysByDomain proves the poll's single read separates the global default
// from a domain's override.
func TestListGatewaysKeysByDomain(t *testing.T) {
	d, _ := freshDirectory(t)
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
	d, _ := freshDirectory(t)
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
	d, _ := freshDirectory(t)
	mustSetGateway(t, d, "Tenant.Test", aGateway())

	_, found, err := d.GetSMTPGateway("tenant.test")
	mustNoErr(t, "read the gateway", err)

	wantEq(t, "the mixed-case domain was found lower-cased", found, true)
}

// TestDeleteGatewayRemovesTheOverride proves removing a domain's row returns it to the
// global gateway.
func TestDeleteGatewayRemovesTheOverride(t *testing.T) {
	d, _ := freshDirectory(t)
	mustSetGateway(t, d, "tenant.test", aGateway())

	removed, err := d.DeleteSMTPGateway("tenant.test")
	mustNoErr(t, "delete the override", err)

	wantEq(t, "the delete removed the override", removed, true)
}

// TestDeleteGatewayReportsNothingRemoved proves deleting an absent gateway is not an error
// and reports honestly.
func TestDeleteGatewayReportsNothingRemoved(t *testing.T) {
	d, _ := freshDirectory(t)

	removed, err := d.DeleteSMTPGateway("tenant.test")
	mustNoErr(t, "delete an absent gateway", err)

	wantEq(t, "nothing was removed", removed, false)
}

package directory

import "testing"

// TestDomainBrandingRoundtrip proves a domain's login branding stores and reads back
// per domain, that an unset domain reports no branding (so the caller serves the
// global default), and that clearing every field removes the override rather than
// persisting an empty record.
func TestDomainBrandingRoundtrip(t *testing.T) {
	d, _ := freshDirectory(t)
	mustCreateDomain(t, d, t.TempDir(), "hermex.test")
	branding := func(what string) (DomainBranding, bool) {
		t.Helper()
		got, has, err := d.GetDomainBranding("hermex.test")
		mustNoErr(t, what, err)
		return got, has
	}

	// A fresh domain has no branding and inherits the default.
	_, has := branding("read the branding of a fresh domain")
	wantEq(t, "a fresh domain has branding", has, false)

	want := DomainBranding{AppName: "Acme Mail", PrimaryColor: "#ff0000", Tagline: "Mail by Acme"}
	mustNoErr(t, "set the branding", d.SetDomainBranding("hermex.test", want))
	got, has := branding("read the branding back")
	wantEq(t, "the domain has branding after the set", has, true)
	wantEq(t, "the branding", got, want)

	// Clearing every field removes the override so the domain inherits the default.
	mustNoErr(t, "clear the branding", d.SetDomainBranding("hermex.test", DomainBranding{}))
	_, has = branding("read the branding after clearing it")
	wantEq(t, "the domain still has branding after clearing every field", has, false)
}

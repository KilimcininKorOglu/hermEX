package directory

import (
	"database/sql"
	"strings"
	"testing"
)

// TestBrandingTaglineMigration proves the upgrade drops the tagline the login page
// no longer shows from every stored branding blob, keeps the other fields, and
// clears a blob that held only a tagline so the domain inherits the default.
func TestBrandingTaglineMigration(t *testing.T) {
	d, db := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "only.test")
	mustCreateDomain(t, d, root, "mixed.test")
	for domain, blob := range map[string]string{
		"only.test":  `{"tagline":"Mail by Acme"}`,
		"mixed.test": `{"app_name":"Acme Mail","tagline":"Mail by Acme"}`,
	} {
		_, err := db.Exec(`UPDATE domains SET branding_json = ? WHERE domainname = ?`, blob, domain)
		mustNoErr(t, "store the old blob of "+domain, err)
	}
	_, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 59`)
	mustNoErr(t, "mark the migration as not applied", err)
	mustNoErr(t, "apply the migration", d.EnsureSchema())

	stored := func(domain string) sql.NullString {
		t.Helper()
		var raw sql.NullString
		mustNoErr(t, "read the blob of "+domain,
			db.QueryRow(`SELECT branding_json FROM domains WHERE domainname = ?`, domain).Scan(&raw))
		return raw
	}
	wantEq(t, "a tagline-only blob is cleared", stored("only.test").Valid, false)
	got, has, err := d.GetDomainBranding("mixed.test")
	mustNoErr(t, "read the mixed branding", err)
	wantEq(t, "the mixed domain keeps its branding", has, true)
	wantEq(t, "the mixed branding", got, DomainBranding{AppName: "Acme Mail"})
	if raw := stored("mixed.test"); raw.Valid && strings.Contains(raw.String, "tagline") {
		t.Errorf("the tagline is still stored: %s", raw.String)
	}
}

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

	want := DomainBranding{AppName: "Acme Mail", PrimaryColor: "#ff0000", FooterText: "Mail by Acme"}
	mustNoErr(t, "set the branding", d.SetDomainBranding("hermex.test", want))
	got, has := branding("read the branding back")
	wantEq(t, "the domain has branding after the set", has, true)
	wantEq(t, "the branding", got, want)

	// Clearing every field removes the override so the domain inherits the default.
	mustNoErr(t, "clear the branding", d.SetDomainBranding("hermex.test", DomainBranding{}))
	_, has = branding("read the branding after clearing it")
	wantEq(t, "the domain still has branding after clearing every field", has, false)
}

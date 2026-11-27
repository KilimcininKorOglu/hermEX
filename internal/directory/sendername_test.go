package directory

import "testing"

// TestFormatSenderName proves the template substitution and tidy-up: full values
// render verbatim, absent values drop the parenthetical group or trim the separator
// they left dangling, a non-parenthetical trailing separator is trimmed, and an
// empty template yields "" (leave the From name untouched).
func TestFormatSenderName(t *testing.T) {
	cases := []struct {
		name string
		tpl  string
		vals map[string]string
		want string
	}{
		{"full", "{name} ({company} - {title})",
			map[string]string{"name": "Ali Veli", "company": "Acme", "title": "Sales"}, "Ali Veli (Acme - Sales)"},
		{"absent extras drop the group", "{name} ({company} - {title})",
			map[string]string{"name": "Ali Veli"}, "Ali Veli"},
		{"company only trims dangling separator", "{name} ({company} - {title})",
			map[string]string{"name": "Ali Veli", "company": "Acme"}, "Ali Veli (Acme)"},
		{"title only trims leading separator", "{name} ({company} - {title})",
			map[string]string{"name": "Ali Veli", "title": "Sales"}, "Ali Veli (Sales)"},
		{"non-paren trailing separator trimmed", "{name} | {department}",
			map[string]string{"name": "Ali Veli"}, "Ali Veli"},
		{"non-paren full", "{name} | {department}",
			map[string]string{"name": "Ali Veli", "department": "IT"}, "Ali Veli | IT"},
		{"empty template", "", map[string]string{"name": "Ali Veli"}, ""},
		{"whitespace template", "   ", map[string]string{"name": "Ali Veli"}, ""},
		// Only the documented placeholders expand: a template naming any other key
		// must not pull an arbitrary profile value into the From header.
		{"unknown placeholder stays literal", "{name} {password}",
			map[string]string{"name": "Ali Veli", "password": "hunter2"}, "Ali Veli {password}"},
	}
	for _, c := range cases {
		if got := FormatSenderName(c.tpl, c.vals); got != c.want {
			t.Errorf("%s: FormatSenderName(%q) = %q, want %q", c.name, c.tpl, got, c.want)
		}
	}
}

// TestDomainNameTemplatesRoundtrip proves the per-domain internal and external
// templates store and read back independently, and an unknown domain yields empty
// strings (no customization).
func TestDomainNameTemplatesRoundtrip(t *testing.T) {
	d := freshSQL(t)
	in, ex, err := d.GetDomainNameTemplates("ghost.test")
	mustNoErr(t, "unknown domain templates", err)
	wantEq(t, "unknown domain internal template", in, "")
	wantEq(t, "unknown domain external template", ex, "")
	seedDomain(t, d, t.TempDir(), "hermex.test")
	mustNoErr(t, "set templates", d.SetDomainNameTemplates("hermex.test", "{name} ({department})", "{name} ({company} - {title})"))
	in, ex, err = d.GetDomainNameTemplates("hermex.test")
	mustNoErr(t, "get templates", err)
	wantEq(t, "internal template", in, "{name} ({department})")
	wantEq(t, "external template", ex, "{name} ({company} - {title})")
}

// TestOutgoingDisplayNames proves the sender's domain templates and profile combine
// into the internal and external From display names, and that a domain with no
// templates yields empty strings (no customization, no profile read needed).
func TestOutgoingDisplayNames(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "hermex.test")
	seedUser(t, d, root, "ali@hermex.test", "password1")
	ok, err := d.SetUserProperties("ali@hermex.test", map[uint32]string{
		0x3001001F: "Ali Veli", 0x3A16001F: "Acme", 0x3A17001F: "Sales",
	})
	mustNoErr(t, "set the profile fields", err)
	wantEq(t, "SetUserProperties found ali", ok, true)
	names := func() (internal, external string) {
		t.Helper()
		in, ex, err := d.OutgoingDisplayNames("ali@hermex.test")
		mustNoErr(t, "resolve the display names", err)
		return in, ex
	}

	in, ex := names()
	wantEq(t, "the internal name with no templates", in, "")
	wantEq(t, "the external name with no templates", ex, "")

	mustNoErr(t, "set the domain templates",
		d.SetDomainNameTemplates("hermex.test", "{name} ({title})", "{name} ({company} - {title})"))
	in, ex = names()
	wantEq(t, "the internal display name", in, "Ali Veli (Sales)")
	wantEq(t, "the external display name", ex, "Ali Veli (Acme - Sales)")
}

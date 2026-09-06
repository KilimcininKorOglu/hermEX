package directory

import (
	"path/filepath"
	"testing"
)

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
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if in, ex, err := d.GetDomainNameTemplates("ghost.test"); err != nil || in != "" || ex != "" {
		t.Fatalf("unknown domain = (%q,%q,%v), want empty", in, ex, err)
	}
	if _, err := d.CreateDomain("hermex.test", filepath.Join(t.TempDir(), "dom")); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDomainNameTemplates("hermex.test", "{name} ({department})", "{name} ({company} - {title})"); err != nil {
		t.Fatal(err)
	}
	in, ex, err := d.GetDomainNameTemplates("hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if in != "{name} ({department})" || ex != "{name} ({company} - {title})" {
		t.Errorf("templates = (%q, %q), want the internal and external forms", in, ex)
	}
}

// TestOutgoingDisplayNames proves the sender's domain templates and profile combine
// into the internal and external From display names, and that a domain with no
// templates yields empty strings (no customization, no profile read needed).
func TestOutgoingDisplayNames(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "ali@hermex.test", "pw")
	_, err := d.SetUserProperties("ali@hermex.test", map[uint32]string{
		0x3001001F: "Ali Veli", 0x3A16001F: "Acme", 0x3A17001F: "Sales",
	})
	mustNoErr(t, "set the profile fields", err)
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

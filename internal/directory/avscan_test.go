package directory

import "testing"

// TestDomainAVScan proves the per-domain antivirus toggles default off, persist,
// and are read case-insensitively, and an unknown domain reads as off.
func TestDomainAVScan(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	seedDomain(t, d, root, "acme.test")
	scan := func(domain string) (inbound, outbound bool) {
		t.Helper()
		in, out, err := d.GetDomainAVScan(domain)
		mustNoErr(t, "read the AV toggles", err)
		return in, out
	}

	in, out := scan("acme.test")
	wantEq(t, "inbound scanning by default", in, false)
	wantEq(t, "outbound scanning by default", out, false)

	// Set via a mixed-case name and read via lowercase: an operator may type the
	// domain in any case. The column's collation already compares without case,
	// so this does not isolate the ToLower call.
	mustNoErr(t, "set the AV toggles", d.SetDomainAVScan("ACME.test", true, false))
	in, out = scan("acme.test")
	wantEq(t, "inbound scanning after the set", in, true)
	wantEq(t, "outbound scanning after the set", out, false)

	// Another tenant's toggles stay on that tenant.
	seedDomain(t, d, root, "other.test")
	mustNoErr(t, "set the other domain's toggles", d.SetDomainAVScan("other.test", false, true))
	in, out = scan("acme.test")
	wantEq(t, "acme inbound after the other domain's set", in, true)
	wantEq(t, "acme outbound after the other domain's set", out, false)
	in, out = scan("other.test")
	wantEq(t, "other inbound", in, false)
	wantEq(t, "other outbound", out, true)

	in, out = scan("nope.test")
	wantEq(t, "inbound scanning for an unknown domain", in, false)
	wantEq(t, "outbound scanning for an unknown domain", out, false)
}

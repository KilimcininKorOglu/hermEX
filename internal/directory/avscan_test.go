package directory

import "testing"

// TestDomainAVScan proves the per-domain antivirus toggles default off, persist,
// and are read case-insensitively, and an unknown domain reads as off.
func TestDomainAVScan(t *testing.T) {
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "acme.test")
	scan := func(domain string) (inbound, outbound bool) {
		t.Helper()
		in, out, err := d.GetDomainAVScan(domain)
		mustNoErr(t, "read the AV toggles", err)
		return in, out
	}

	in, out := scan("acme.test")
	wantEq(t, "inbound scanning by default", in, false)
	wantEq(t, "outbound scanning by default", out, false)

	// Set via a mixed-case name; read via lowercase proves normalization.
	mustNoErr(t, "set the AV toggles", d.SetDomainAVScan("ACME.test", true, false))
	in, out = scan("acme.test")
	wantEq(t, "inbound scanning after the set", in, true)
	wantEq(t, "outbound scanning after the set", out, false)

	in, out = scan("nope.test")
	wantEq(t, "inbound scanning for an unknown domain", in, false)
	wantEq(t, "outbound scanning for an unknown domain", out, false)
}

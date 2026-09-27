package directory

import "testing"

// TestSplitRelayHost proves a domain is not split by default, that the host
// persists normalized, and that clearing it turns the split off.
func TestSplitRelayHost(t *testing.T) {
	d, _ := freshDirectory(t)
	mustCreateDomain(t, d, t.TempDir(), "acme.test")

	host, err := d.SplitRelayHost("acme.test")
	mustNoErr(t, "read the split host", err)
	wantEq(t, "split host by default", host, "")

	mustNoErr(t, "set the split host", d.SetSplitRelayHost("ACME.test", "Legacy.Acme.test"))
	host, err = d.SplitRelayHost("acme.test")
	mustNoErr(t, "read the split host", err)
	wantEq(t, "split host after the set", host, "legacy.acme.test")

	mustNoErr(t, "clear the split host", d.SetSplitRelayHost("acme.test", ""))
	host, err = d.SplitRelayHost("acme.test")
	mustNoErr(t, "read the split host", err)
	wantEq(t, "split host after clearing", host, "")
}

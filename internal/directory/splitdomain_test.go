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

// TestSplitRelayHostRefusalKeepsTheStoredOne proves a host the relay could not dial
// (a line break, a port, an empty label) and an unknown domain are refused, and that
// a refusal leaves the stored host as it was.
func TestSplitRelayHostRefusalKeepsTheStoredOne(t *testing.T) {
	d, _ := freshDirectory(t)
	mustCreateDomain(t, d, t.TempDir(), "acme.test")
	mustNoErr(t, "set the split host", d.SetSplitRelayHost("acme.test", "legacy.acme.test"))
	for _, bad := range []string{"legacy.acme.test\r\nRCPT TO:<x@evil.example>", "legacy.acme.test:25", "legacy..acme.test", "-legacy.acme.test"} {
		wantErr(t, "the split host "+bad+" was accepted", d.SetSplitRelayHost("acme.test", bad))
		host, err := d.SplitRelayHost("acme.test")
		mustNoErr(t, "read the split host", err)
		wantEq(t, "split host after refusing "+bad, host, "legacy.acme.test")
	}
	wantErr(t, "a split host for an unknown domain was accepted", d.SetSplitRelayHost("nowhere.test", "legacy.acme.test"))
	mustNoErr(t, "re-save the same host", d.SetSplitRelayHost("acme.test", "legacy.acme.test"))
}

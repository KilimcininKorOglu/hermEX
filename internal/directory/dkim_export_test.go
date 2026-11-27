package directory

import (
	"bytes"
	"testing"
)

// dkimTestDir seeds one hosted domain, the smallest directory a signing key can
// belong to.
func dkimTestDir(t *testing.T) *SQLDirectory {
	t.Helper()
	d := freshSQL(t)
	seedDomain(t, d, t.TempDir(), "acme.test")
	return d
}

// testPEM stands in for a generated signing key; the export path never parses it.
var testPEM = []byte("-----BEGIN PRIVATE KEY-----\nnot-a-real-key\n-----END PRIVATE KEY-----\n")

// TestExportDKIMKeyReachesAnUnpublishedKey is the whole point of the export path.
// A key is stored disabled until its DNS record is published, and the signing
// lookup refuses a disabled key so an unpublished one never signs. That leaves
// exactly the key whose loss goes unnoticed longest with no way out of the
// database, which the panel only ever writes into.
func TestExportDKIMKeyReachesAnUnpublishedKey(t *testing.T) {
	d := dkimTestDir(t)
	if err := d.SetDKIMKey("acme.test", "hermex", testPEM, "v=DKIM1; p=AAAA"); err != nil {
		t.Fatal(err)
	}

	// The signing lookup refuses it, which is correct and is why the export path
	// has to exist.
	if pem, sel, found, err := d.DKIMKey("acme.test"); err != nil || found || pem != nil || sel != "" {
		t.Fatalf("a freshly generated key is signing already (pem=%q sel=%q found=%v err=%v)", pem, sel, found, err)
	}

	got, selector, found, err := d.ExportDKIMKey("acme.test")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the key cannot be exported, so the database is still its only copy")
	}
	if !bytes.Equal(got, testPEM) {
		t.Errorf("exported key = %q, want the stored one", got)
	}
	if selector != "hermex" {
		t.Errorf("selector = %q, want hermex", selector)
	}
}

// TestExportDKIMKeyReturnsTheEnabledKeyToo proves the export does not swap one
// blind spot for another: a key that is live and signing must be recoverable as
// well, since that is the one a deliverability outage depends on.
func TestExportDKIMKeyReturnsTheEnabledKeyToo(t *testing.T) {
	d := dkimTestDir(t)
	if err := d.SetDKIMKey("acme.test", "hermex", testPEM, "v=DKIM1; p=AAAA"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDKIMEnabled("acme.test", true); err != nil {
		t.Fatal(err)
	}
	got, sel, found, err := d.ExportDKIMKey("acme.test")
	if err != nil || !found || sel != "hermex" || !bytes.Equal(got, testPEM) {
		t.Errorf("export of an enabled key = (%q, %q, %v, %v), want the stored key", got, sel, found, err)
	}
}

// TestExportDKIMKeyReportsAbsence proves a domain with no key is reported as
// having none rather than answering with an empty key, so a backup script cannot
// mistake nothing for something.
func TestExportDKIMKeyReportsAbsence(t *testing.T) {
	d := dkimTestDir(t)
	got, selector, found, err := d.ExportDKIMKey("acme.test")
	if err != nil {
		t.Fatal(err)
	}
	if found || got != nil || selector != "" {
		t.Errorf("export with no key = (%q, %q, %v), want nothing found", got, selector, found)
	}
}

// TestExportDKIMKeyIsPerDomain stores a key for one domain and exports another.
// A signing key lets its holder sign mail as that domain, so one tenant's export
// must never answer with another tenant's key.
func TestExportDKIMKeyIsPerDomain(t *testing.T) {
	d := dkimTestDir(t)
	seedDomain(t, d, t.TempDir(), "other.test")
	if err := d.SetDKIMKey("acme.test", "hermex", testPEM, "v=DKIM1; p=AAAA"); err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"other.test", "unhosted.test", "", "%"} {
		got, sel, found, err := d.ExportDKIMKey(domain)
		if err != nil {
			t.Fatalf("export %q: %v", domain, err)
		}
		if found || got != nil || sel != "" {
			t.Errorf("export %q returned (%q, %q, %v), want empty", domain, got, sel, found)
		}
	}
}

// TestExportDKIMKeySurvivesARegeneration proves the export always reflects the
// current key: regenerating replaces it, and an operator exporting afterwards
// must get the new one, not the copy the old DNS record matched.
func TestExportDKIMKeySurvivesARegeneration(t *testing.T) {
	d := dkimTestDir(t)
	if err := d.SetDKIMKey("acme.test", "hermex", testPEM, "v=DKIM1; p=AAAA"); err != nil {
		t.Fatal(err)
	}
	second := []byte("-----BEGIN PRIVATE KEY-----\nsecond-key\n-----END PRIVATE KEY-----\n")
	if err := d.SetDKIMKey("acme.test", "hermex", second, "v=DKIM1; p=BBBB"); err != nil {
		t.Fatal(err)
	}
	got, sel, found, err := d.ExportDKIMKey("acme.test")
	if err != nil || !found || sel != "hermex" {
		t.Fatalf("ExportDKIMKey = (sel=%q, found=%v, err=%v)", sel, found, err)
	}
	if !bytes.Equal(got, second) {
		t.Errorf("exported key = %q, want the regenerated one", got)
	}
}

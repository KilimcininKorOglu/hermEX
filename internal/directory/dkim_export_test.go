package directory

import (
	"bytes"
	"path/filepath"
	"testing"
)

// dkimTestDir seeds one hosted domain, the smallest directory a signing key can
// belong to.
func dkimTestDir(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := d.CreateDomain("acme.test", filepath.Join(t.TempDir(), "dom")); err != nil {
		t.Fatal(err)
	}
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
	if _, _, found, err := d.DKIMKey("acme.test"); err != nil || found {
		t.Fatalf("a freshly generated key is signing already (found=%v, err=%v)", found, err)
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
	got, _, found, err := d.ExportDKIMKey("acme.test")
	if err != nil || !found || !bytes.Equal(got, testPEM) {
		t.Errorf("export of an enabled key = (%q, %v, %v), want the stored key", got, found, err)
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
	got, _, _, err := d.ExportDKIMKey("acme.test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, second) {
		t.Errorf("exported key = %q, want the regenerated one", got)
	}
}

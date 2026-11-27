package directory

import (
	"testing"

	"hermex/internal/logging"
)

func setupDKIM(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	// cleanTables already emptied dkim_keys, so this delete finds nothing.
	execRows(t, "clear the DKIM keys", 0)(db.Exec("DELETE FROM dkim_keys"))
	return d
}

// TestDKIMKeyDisabledUntilEnabled is the load-bearing test: a stored key is NOT handed
// to the signer until it is explicitly enabled, so generating a key never starts
// producing DKIM=fail before the operator publishes the DNS record.
func TestDKIMKeyDisabledUntilEnabled(t *testing.T) {
	d := setupDKIM(t)
	mustNoErr(t, "SetDKIMKey", d.SetDKIMKey("example.com", "sel1", []byte("PRIVATE-PEM"), "v=DKIM1; k=rsa; p=AAA"))

	// Stored but not enabled -> the signer gets nothing.
	wantSigningKey(t, d, "example.com", false)

	// Enable -> now the signer gets the key and selector.
	mustNoErr(t, "SetDKIMEnabled", d.SetDKIMEnabled("example.com", true))
	pem, sel, found, err := d.DKIMKey("example.com")
	mustNoErr(t, "DKIMKey", err)
	wantEq(t, "DKIMKey found", found, true)
	wantEq(t, "DKIMKey pem", string(pem), "PRIVATE-PEM")
	wantEq(t, "DKIMKey selector", sel, "sel1")

	// Case-insensitive lookup (DKIM domains are case-insensitive).
	wantSigningKey(t, d, "EXAMPLE.COM", true)
}

// wantSigningKey asserts whether the signer is handed a key for domain.
func wantSigningKey(t *testing.T, d *SQLDirectory, domain string, want bool) {
	t.Helper()
	pem, sel, found, err := d.DKIMKey(domain)
	mustNoErr(t, "signing lookup for "+domain, err)
	wantEq(t, "signing key for "+domain, found, want)
	if want && (len(pem) == 0 || sel == "") {
		t.Errorf("signing lookup for %s returned empty pem/sel (%q, %q)", domain, pem, sel)
	}
	if !want && (pem != nil || sel != "") {
		t.Errorf("absent signing lookup for %s returned (%q, %q)", domain, pem, sel)
	}
}

// mustDKIMInfo reads a domain's key metadata.
func mustDKIMInfo(t *testing.T, d *SQLDirectory, domain string) (DKIMKeyInfo, bool) {
	t.Helper()
	info, found, err := d.GetDKIMKeyInfo(domain)
	mustNoErr(t, "read key info for "+domain, err)
	return info, found
}

// TestDKIMKeyInfo proves the display metadata reads back without exposing the private
// key, and reports not-found for a domain with no key.
func TestDKIMKeyInfo(t *testing.T) {
	d := setupDKIM(t)
	if empty, found, err := d.GetDKIMKeyInfo("none.test"); err != nil || found || empty != (DKIMKeyInfo{}) {
		t.Fatalf("Get on a keyless domain = %+v found %v err %v, want zero/not found", empty, found, err)
	}
	if err := d.SetDKIMKey("example.com", "sel1", []byte("PRIVATE-PEM"), "v=DKIM1; k=rsa; p=PUB"); err != nil {
		t.Fatal(err)
	}
	info, found, err := d.GetDKIMKeyInfo("example.com")
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v", found, err)
	}
	if info.Selector != "sel1" || info.PublicTXT != "v=DKIM1; k=rsa; p=PUB" || info.Enabled {
		t.Errorf("info = %+v, want sel1 / the TXT / disabled", info)
	}
}

// TestDKIMRegenerateResetsEnabled proves replacing a key disables it again, so a
// rotated key cannot keep signing before its new DNS record is published.
func TestDKIMRegenerateResetsEnabled(t *testing.T) {
	d := setupDKIM(t)
	mustNoErr(t, "store the old key", d.SetDKIMKey("example.com", "sel1", []byte("OLD"), "txt-old"))
	mustNoErr(t, "enable the old key", d.SetDKIMEnabled("example.com", true))
	wantSigningKey(t, d, "example.com", true)

	if err := d.SetDKIMKey("example.com", "sel2", []byte("NEW"), "txt-new"); err != nil {
		t.Fatal(err)
	}
	wantSigningKey(t, d, "example.com", false)
	info, found := mustDKIMInfo(t, d, "example.com")
	if !found || info.Selector != "sel2" || info.Enabled {
		t.Errorf("after regenerate = %+v (found %v), want sel2 / disabled", info, found)
	}
}

// TestDKIMChangesStayOnTheirDomain enables and deletes one domain's key while
// another domain holds one: a signing key lets its holder sign as that domain,
// so neither change may reach the other tenant's key.
func TestDKIMChangesStayOnTheirDomain(t *testing.T) {
	d := setupDKIM(t)
	mustNoErr(t, "store example.com", d.SetDKIMKey("example.com", "sel1", []byte("PEM"), "txt"))
	mustNoErr(t, "store other.test", d.SetDKIMKey("other.test", "sel1", []byte("OTHER"), "txt"))

	mustNoErr(t, "enable example.com", d.SetDKIMEnabled("example.com", true))
	wantSigningKey(t, d, "other.test", false)

	mustNoErr(t, "delete example.com", d.DeleteDKIMKey("example.com"))
	if gone, found := mustDKIMInfo(t, d, "example.com"); found || gone != (DKIMKeyInfo{}) {
		t.Errorf("key should be gone after delete: %+v found=%v", gone, found)
	}
	if other, found := mustDKIMInfo(t, d, "other.test"); !found || other.Selector != "sel1" {
		t.Errorf("deleting example.com's key touched other.test's key: %+v found=%v", other, found)
	}
}

// TestDKIMRewrapFailureIsLogged proves a failed best-effort re-wrap update
// leaves the readable key usable and records a directory warning event.
func TestDKIMRewrapFailureIsLogged(t *testing.T) {
	d := setupDKIM(t)
	sink := &captureSink{}
	d.SetLogger(logging.New(sink))

	mustNoErr(t, "store plaintext key", d.SetDKIMKey("example.com", "sel1", []byte("PRIVATE-PEM"), "txt"))
	mustNoErr(t, "enable key", d.SetDKIMEnabled("example.com", true))
	d.SetKeySecret([]byte("a-long-operator-secret"))
	installFailTrigger(t, d.db, "test_dkim_rewrap_fail", "UPDATE", "dkim_keys")

	pem, sel, found, err := d.DKIMKey("example.com")
	mustNoErr(t, "DKIMKey with failing rewrap", err)
	wantEq(t, "DKIMKey found", found, true)
	wantEq(t, "DKIMKey selector", sel, "sel1")
	wantEq(t, "DKIMKey PEM", string(pem), "PRIVATE-PEM")

	e := wantWarnEvent(t, sink, "directory.dkim_rewrap.fail")
	wantEq(t, "event domain", e.Fields["domain"], "example.com")
}

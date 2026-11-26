package directory

import "testing"

func setupDKIM(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM dkim_keys"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestDKIMKeyDisabledUntilEnabled is the load-bearing test: a stored key is NOT handed
// to the signer until it is explicitly enabled, so generating a key never starts
// producing DKIM=fail before the operator publishes the DNS record.
func TestDKIMKeyDisabledUntilEnabled(t *testing.T) {
	d := setupDKIM(t)
	if err := d.SetDKIMKey("example.com", "sel1", []byte("PRIVATE-PEM"), "v=DKIM1; k=rsa; p=AAA"); err != nil {
		t.Fatal(err)
	}

	// Stored but not enabled → the signer gets nothing.
	if _, _, found, err := d.DKIMKey("example.com"); err != nil || found {
		t.Fatalf("a stored-but-disabled key must not be returned to the signer: found=%v err=%v", found, err)
	}

	// Enable → now the signer gets the key and selector.
	if err := d.SetDKIMEnabled("example.com", true); err != nil {
		t.Fatal(err)
	}
	pem, sel, found, err := d.DKIMKey("example.com")
	if err != nil || !found {
		t.Fatalf("an enabled key must be returned: found=%v err=%v", found, err)
	}
	if string(pem) != "PRIVATE-PEM" || sel != "sel1" {
		t.Errorf("got key %q selector %q, want PRIVATE-PEM/sel1", pem, sel)
	}

	// Case-insensitive lookup (DKIM domains are case-insensitive).
	wantSigningKey(t, d, "EXAMPLE.COM", true)
}

// wantSigningKey asserts whether the signer is handed a key for domain.
func wantSigningKey(t *testing.T, d *SQLDirectory, domain string, want bool) {
	t.Helper()
	_, _, found, err := d.DKIMKey(domain)
	mustNoErr(t, "signing lookup for "+domain, err)
	wantEq(t, "signing key for "+domain, found, want)
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
	if _, found, err := d.GetDKIMKeyInfo("none.test"); err != nil || found {
		t.Fatalf("Get on a keyless domain = found %v err %v, want not found", found, err)
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
	if _, found := mustDKIMInfo(t, d, "example.com"); found {
		t.Error("key should be gone after delete")
	}
	if _, found := mustDKIMInfo(t, d, "other.test"); !found {
		t.Error("deleting example.com's key removed other.test's key")
	}
}

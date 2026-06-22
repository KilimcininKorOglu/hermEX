package directory

import "testing"

func setupTLSCerts(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	if _, err := db.Exec("DELETE FROM tls_certs"); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestTLSCertUpsertAndLoad proves a stored certificate round-trips with its key for
// the in-process provider, and that storing the same name again replaces it rather
// than adding a row — the listener must present exactly the latest uploaded
// material, so a renewal cannot leave the previous certificate behind.
func TestTLSCertUpsertAndLoad(t *testing.T) {
	d := setupTLSCerts(t)
	if err := d.SetTLSCert("", "CERT-PEM-1", "KEY-PEM-1", 1000); err != nil {
		t.Fatal(err)
	}
	certs, err := d.LoadTLSCerts()
	if err != nil || len(certs) != 1 {
		t.Fatalf("LoadTLSCerts = %d certs, err=%v; want exactly 1", len(certs), err)
	}
	if certs[0].CertPEM != "CERT-PEM-1" || certs[0].KeyPEM != "KEY-PEM-1" {
		t.Errorf("loaded cert = %+v, want the stored chain and key verbatim", certs[0])
	}
	// Re-store the default name with new material: a replacement, not a second row.
	if err := d.SetTLSCert("", "CERT-PEM-2", "KEY-PEM-2", 2000); err != nil {
		t.Fatal(err)
	}
	certs, _ = d.LoadTLSCerts()
	if len(certs) != 1 || certs[0].KeyPEM != "KEY-PEM-2" {
		t.Fatalf("after re-store: %d row(s), key=%q; want 1 row carrying the new key", len(certs), certs[0].KeyPEM)
	}
}

// TestTLSCertListOmitsKey proves the admin-facing list returns only display
// metadata (name and expiry), never key material — the panel must not be able to
// read back a stored private key.
func TestTLSCertListOmitsKey(t *testing.T) {
	d := setupTLSCerts(t)
	if err := d.SetTLSCert("mail.example.com", "CERT-PEM", "SECRET-KEY-PEM", 1234); err != nil {
		t.Fatal(err)
	}
	infos, err := d.ListTLSCerts()
	if err != nil || len(infos) != 1 {
		t.Fatalf("ListTLSCerts = %d, err=%v; want 1", len(infos), err)
	}
	if infos[0].Name != "mail.example.com" || infos[0].NotAfter != 1234 {
		t.Errorf("info = %+v, want the stored name and expiry", infos[0])
	}
}

// TestTLSCertVersionDetectsWriteAndDelete proves the poll probe moves on both an
// upsert and a delete, so the provider reloads on either: a renewal advances the
// version, and a removal lowers the count even though it does not advance the max.
func TestTLSCertVersionDetectsWriteAndDelete(t *testing.T) {
	d := setupTLSCerts(t)
	v0, c0, err := d.TLSCertVersion()
	if err != nil {
		t.Fatal(err)
	}
	if c0 != 0 {
		t.Fatalf("empty store count = %d, want 0", c0)
	}
	if err := d.SetTLSCert("", "C", "K", 1); err != nil {
		t.Fatal(err)
	}
	v1, c1, _ := d.TLSCertVersion()
	if v1 <= v0 || c1 != 1 {
		t.Errorf("after write: version=%d count=%d, want version > %d and count 1", v1, c1, v0)
	}
	if err := d.DeleteTLSCert(""); err != nil {
		t.Fatal(err)
	}
	if _, c2, _ := d.TLSCertVersion(); c2 != 0 {
		t.Errorf("after delete: count=%d, want 0 so the provider sees the row gone", c2)
	}
}

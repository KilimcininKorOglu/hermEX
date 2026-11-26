package directory

import "testing"

func setupTLSCerts(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	mustNoErr(t, "ensure the schema", d.EnsureSchema())
	cleanTables(t, db)
	_, err := db.Exec("DELETE FROM tls_certs")
	mustNoErr(t, "clear the certificates", err)
	return d
}

// mustLoadTLSCerts reads every stored certificate and requires the read to succeed.
func mustLoadTLSCerts(t *testing.T, d *SQLDirectory) []TLSCertData {
	t.Helper()
	certs, err := d.LoadTLSCerts()
	mustNoErr(t, "load the certificates", err)
	return certs
}

// mustTLSCertVersion reads the poll probe and requires the read to succeed.
func mustTLSCertVersion(t *testing.T, d *SQLDirectory) (int64, int64) {
	t.Helper()
	v, c, err := d.TLSCertVersion()
	mustNoErr(t, "read the certificate version", err)
	return v, c
}

// TestTLSCertUpsertAndLoad proves a stored certificate round-trips with its key for
// the in-process provider, and that storing the same name again replaces it rather
// than adding a row, the listener must present exactly the latest uploaded
// material, so a renewal cannot leave the previous certificate behind.
func TestTLSCertUpsertAndLoad(t *testing.T) {
	d := setupTLSCerts(t)
	mustNoErr(t, "store the first certificate", d.SetTLSCert("", "CERT-PEM-1", "KEY-PEM-1", 1000))
	certs := mustLoadTLSCerts(t, d)
	wantEq(t, "certificates after the first store", len(certs), 1)
	wantEq(t, "the stored chain", certs[0].CertPEM, "CERT-PEM-1")
	wantEq(t, "the stored key", certs[0].KeyPEM, "KEY-PEM-1")

	// Re-store the default name with new material: a replacement, not a second row.
	mustNoErr(t, "store the renewal", d.SetTLSCert("", "CERT-PEM-2", "KEY-PEM-2", 2000))
	certs = mustLoadTLSCerts(t, d)
	wantEq(t, "certificates after the renewal", len(certs), 1)
	wantEq(t, "the renewed key", certs[0].KeyPEM, "KEY-PEM-2")
}

// TestTLSCertListOmitsKey proves the admin-facing list returns only display
// metadata (name and expiry), never key material, the panel must not be able to
// read back a stored private key.
func TestTLSCertListOmitsKey(t *testing.T) {
	d := setupTLSCerts(t)
	mustNoErr(t, "store the certificate", d.SetTLSCert("mail.example.com", "CERT-PEM", "SECRET-KEY-PEM", 1234))
	infos, err := d.ListTLSCerts()
	mustNoErr(t, "list the certificates", err)
	wantEq(t, "listed certificates", len(infos), 1)
	wantEq(t, "the listed name", infos[0].Name, "mail.example.com")
	wantEq(t, "the listed expiry", infos[0].NotAfter, int64(1234))
}

// TestTLSCertVersionDetectsWriteAndDelete proves the poll probe moves on both an
// upsert and a delete, so the provider reloads on either: a renewal advances the
// version, and a removal lowers the count even though it does not advance the max.
func TestTLSCertVersionDetectsWriteAndDelete(t *testing.T) {
	d := setupTLSCerts(t)
	v0, c0 := mustTLSCertVersion(t, d)
	wantEq(t, "count of an empty store", c0, int64(0))
	mustNoErr(t, "store a certificate", d.SetTLSCert("", "C", "K", 1))
	v1, c1 := mustTLSCertVersion(t, d)
	if v1 <= v0 {
		t.Errorf("version after a write = %d, want above %d", v1, v0)
	}
	wantEq(t, "count after a write", c1, int64(1))
	mustNoErr(t, "delete the certificate", d.DeleteTLSCert(""))
	_, c2 := mustTLSCertVersion(t, d)
	wantEq(t, "count after the delete", c2, int64(0))
}

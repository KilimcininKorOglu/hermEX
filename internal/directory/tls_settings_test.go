package directory

import "testing"

func setupTLSSettings(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	mustNoErr(t, "ensure the schema", d.EnsureSchema())
	cleanTables(t, db)
	res, err := db.Exec("DELETE FROM tls_settings")
	mustNoErr(t, "clear the TLS settings", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared rows", err)
	wantEq(t, "at most the one settings row was cleared", cleared <= 1, true)
	return d
}

// TestTLSSettingsDefaultsToManual proves an unconfigured deployment reads as manual
// mode rather than erroring, an upgrade must never silently flip a running server
// into reaching out to a CA, so the absent row is the safe default.
func TestTLSSettingsDefaultsToManual(t *testing.T) {
	d := setupTLSSettings(t)
	s, found, err := d.GetTLSSettings()
	mustNoErr(t, "read the TLS settings", err)
	wantEq(t, "found on an empty table", found, false)
	wantEq(t, "default mode", s.Mode, "manual")
}

// TestTLSSettingsRoundTripAndUpsert proves the ACME configuration round-trips,
// including the agreed flag, and that saving again replaces the single row rather
// than adding one, so the gateway always reads exactly one current mode.
func TestTLSSettingsRoundTripAndUpsert(t *testing.T) {
	d := setupTLSSettings(t)
	want := TLSSettings{Mode: "acme", ACMEEmail: "ops@example.com", ACMECAURL: "https://acme-staging.example/dir", ACMEAgreed: true}
	mustNoErr(t, "save the ACME settings", d.SetTLSSettings(want))
	got, found, err := d.GetTLSSettings()
	mustNoErr(t, "read the TLS settings", err)
	wantEq(t, "a saved row was found", found, true)
	wantEq(t, "round-trip", got, want)

	// Re-save with manual mode and ToS withdrawn: a replacement, not a second row.
	mustNoErr(t, "save manual mode", d.SetTLSSettings(TLSSettings{Mode: "manual"}))
	got, found, err = d.GetTLSSettings()
	mustNoErr(t, "read the TLS settings again", err)
	wantEq(t, "saved row still found", found, true)
	wantEq(t, "mode after re-save", got.Mode, "manual")
	wantEq(t, "agreed after re-save", got.ACMEAgreed, false)
	var n int
	mustNoErr(t, "count the rows", d.db.QueryRow("SELECT COUNT(*) FROM tls_settings").Scan(&n))
	wantEq(t, "rows after two saves", n, 1)
}

// TestTLSSettingsRefusalKeepsTheStoredOne proves a mode the gateway does not know,
// and an ACME mode without the contact address or the accepted terms a CA account
// requires (RFC 8555 section 7.3), are refused and leave the stored row as it was.
func TestTLSSettingsRefusalKeepsTheStoredOne(t *testing.T) {
	d := setupTLSSettings(t)
	keep := TLSSettings{Mode: "acme", ACMEEmail: "ops@example.com", ACMEAgreed: true}
	mustNoErr(t, "save the ACME settings", d.SetTLSSettings(keep))
	for name, bad := range map[string]TLSSettings{
		"unknown mode":      {Mode: "selfsigned"},
		"empty mode":        {Mode: ""},
		"mode with newline": {Mode: "manual\n"},
		"acme without mail": {Mode: "acme", ACMEAgreed: true},
		"acme not agreed":   {Mode: "acme", ACMEEmail: "ops@example.com"},
		// RFC 8555 section 6.1: every ACME request is HTTPS, so a directory URL
		// on any other scheme would send the account key's requests in clear.
		"http CA URL":     {Mode: "acme", ACMEEmail: "ops@example.com", ACMEAgreed: true, ACMECAURL: "http://acme.example/dir"},
		"file CA URL":     {Mode: "acme", ACMEEmail: "ops@example.com", ACMEAgreed: true, ACMECAURL: "file:///etc/passwd"},
		"hostless CA URL": {Mode: "acme", ACMEEmail: "ops@example.com", ACMEAgreed: true, ACMECAURL: "https:///dir"},
		// RFC 8555 section 7.3: the contact is a mailto: address, one addr-spec.
		"contact with CRLF": {Mode: "acme", ACMEEmail: "ops@example.com\r\nBcc: x@evil.example", ACMEAgreed: true},
		"contact not addr":  {Mode: "acme", ACMEEmail: "not an address", ACMEAgreed: true},
	} {
		wantErr(t, "the "+name+" was accepted", d.SetTLSSettings(bad))
		got, found, err := d.GetTLSSettings()
		mustNoErr(t, "read the TLS settings after the "+name, err)
		wantEq(t, "row found after refused "+name, found, true)
		wantEq(t, "settings after the refused "+name, got, keep)
	}
}

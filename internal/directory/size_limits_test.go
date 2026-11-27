package directory

import "testing"

func setupSizeLimits(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	res, err := db.Exec("DELETE FROM size_limits")
	mustNoErr(t, "clear the size limits", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared rows", err)
	wantEq(t, "at most the one limits row was cleared", cleared <= 1, true)
	return d
}

// TestSizeLimitsRoundTrip proves a fresh database reports no limits (so each protocol
// keeps its built-in default), and that a saved row reads back.
func TestSizeLimitsRoundTrip(t *testing.T) {
	d := setupSizeLimits(t)

	if empty, found, err := d.GetSizeLimits(); err != nil || found || empty != (SizeLimits{}) {
		t.Fatalf("Get on empty = %+v found %v err %v, want zero not found", empty, found, err)
	}

	// Every field carries a DISTINCT non-zero value, because the assertion below
	// compares the whole struct: a field left at zero round-trips even when its
	// column was never written, so it would prove nothing.
	want := SizeLimits{
		IMAPLiteralBytes: 10485760, EWSRequestBytes: 4194304, ActiveSyncRequestBytes: 2097152,
		DAVICalBytes: 1048576, DAVVCardBytes: 3145728, WebmailRequestBytes: 41943040,
		MapiRequestBytes: 33554432, FreeBusyMaxTargets: 42, WebmailPreviewMaxBytes: 5242880,
		IMAPCommandLineBytes: 32768, POP3CommandLineBytes: 4096, SMTPCommandLineBytes: 1024,
		EWSSubscriptionTimeoutMinutes: 720, TLSReportBytes: 6291456, MapiUploadMessageBytes: 73400320,
	}
	if err := d.SetSizeLimits(want); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetSizeLimits()
	if err != nil || !found {
		t.Fatalf("Get after Set = found %v err %v, want found", found, err)
	}
	if got != want {
		t.Errorf("limits = %+v, want %+v", got, want)
	}
}

// TestSizeLimitsRefuseANegativeValue proves a negative limit is refused and the
// stored row kept. Zero means "the protocol's built-in default"; a negative byte
// count or target count means nothing.
func TestSizeLimitsRefuseANegativeValue(t *testing.T) {
	d := setupSizeLimits(t)
	want := SizeLimits{IMAPLiteralBytes: 1048576, FreeBusyMaxTargets: 10}
	mustNoErr(t, "save the starting limits", d.SetSizeLimits(want))
	// One case per field, so a field dropped from the check is caught.
	for _, bad := range []SizeLimits{
		{IMAPLiteralBytes: -1},
		{EWSRequestBytes: -1},
		{ActiveSyncRequestBytes: -1},
		{DAVICalBytes: -1},
		{DAVVCardBytes: -1},
		{WebmailRequestBytes: -1},
		{MapiRequestBytes: -1},
		{FreeBusyMaxTargets: -1},
		{WebmailPreviewMaxBytes: -1},
		{IMAPCommandLineBytes: -1},
		{POP3CommandLineBytes: -1},
		{SMTPCommandLineBytes: -1},
		{EWSSubscriptionTimeoutMinutes: -5},
		{TLSReportBytes: -1},
		{MapiUploadMessageBytes: -1048576},
	} {
		wantErr(t, "SetSizeLimits accepted a negative limit", d.SetSizeLimits(bad))
		got, found, err := d.GetSizeLimits()
		mustNoErr(t, "read the limits after a refusal", err)
		wantEq(t, "limits found after a refusal", found, true)
		wantEq(t, "limits after a refusal", got, want)
	}
}

// TestSizeLimitsUpsert proves a second save replaces the single row.
func TestSizeLimitsUpsert(t *testing.T) {
	d := setupSizeLimits(t)
	if err := d.SetSizeLimits(SizeLimits{IMAPLiteralBytes: 52428800, EWSRequestBytes: 8388608, ActiveSyncRequestBytes: 4194304}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSizeLimits(SizeLimits{IMAPLiteralBytes: 1048576, EWSRequestBytes: 2097152, ActiveSyncRequestBytes: 3145728}); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.GetSizeLimits()
	if err != nil || !found {
		t.Fatalf("Get after upsert = found %v err %v", found, err)
	}
	if got.IMAPLiteralBytes != 1048576 || got.EWSRequestBytes != 2097152 || got.ActiveSyncRequestBytes != 3145728 {
		t.Errorf("after upsert = %+v, want 1048576 / 2097152 / 3145728", got)
	}
	wantRows(t, d.db, "limits rows after two saves", 1, `SELECT COUNT(*) FROM size_limits`)
}

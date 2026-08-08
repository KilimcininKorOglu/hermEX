package mta

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/antivirus"
	"hermex/internal/directory"
)

// storedScanner points the package scanner at a scripted clamd for one test.
func storedScanner(t *testing.T, addr string, fd *fakeAVDir) {
	t.Helper()
	sc, err := antivirus.New(addr)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	SetScanner(sc, fd, func(id int64) string { return filepath.Join(tmp, fmt.Sprintf("%d.eml", id)) }, "mail.test", nil)
	t.Cleanup(func() { SetScanner(nil, nil, nil, "", nil) })
}

// TestScanStoredQuarantinesAndRefuses covers content a user places directly into
// a mailbox: an EWS attachment, an imported .eml, an IMAP APPEND, a MAPI
// attachment save. None of it passes through delivery, so without this scan an
// authenticated account can park malware in a mailbox and wait for someone to
// open it.
func TestScanStoredQuarantinesAndRefuses(t *testing.T) {
	found := fakeClamd(t, "stream: Eicar-Test-Signature FOUND\x00")
	fd := &fakeAVDir{inbound: true, domainID: 7, known: true}
	storedScanner(t, found, fd)

	blocked := ScanStored(directory.StaticAccounts{}, "victim@acme.test", "invoice.exe",
		[]byte("MZ malware bytes"), time.Unix(1000, 0))
	if !blocked {
		t.Fatal("infected stored content was accepted")
	}
	if len(fd.quarantined) != 1 {
		t.Fatalf("quarantine holds %d record(s), want 1", len(fd.quarantined))
	}
	q := fd.quarantined[0]
	if q.Direction != "stored" {
		t.Errorf("direction = %q, want stored (it was neither received nor sent)", q.Direction)
	}
	if q.Subject != "invoice.exe" {
		t.Errorf("subject = %q, want the item's name", q.Subject)
	}
	if q.MailFrom != "victim@acme.test" {
		t.Errorf("MailFrom = %q, want the account that stored it", q.MailFrom)
	}
}

// TestScanStoredAllowsCleanContent keeps the ordinary path ordinary.
func TestScanStoredAllowsCleanContent(t *testing.T) {
	clean := fakeClamd(t, "stream: OK\x00")
	fd := &fakeAVDir{inbound: true, domainID: 7, known: true}
	storedScanner(t, clean, fd)

	if ScanStored(directory.StaticAccounts{}, "victim@acme.test", "notes.txt", []byte("hello"), time.Unix(1000, 0)) {
		t.Error("clean content was refused")
	}
	if len(fd.quarantined) != 0 {
		t.Errorf("clean content produced %d quarantine record(s)", len(fd.quarantined))
	}
}

// TestScanStoredWithoutAScannerProceeds holds the deployment baseline: with no
// clamd configured, storing content must keep working.
func TestScanStoredWithoutAScannerProceeds(t *testing.T) {
	SetScanner(nil, nil, nil, "", nil)
	if ScanStored(directory.StaticAccounts{}, "victim@acme.test", "x", []byte("anything"), time.Unix(1000, 0)) {
		t.Error("content was refused with no scanner configured")
	}
}

// TestScanStoredIsOffWhenTheDomainToggleIsOff proves the operator's per-domain
// switch governs this path too, rather than it scanning unconditionally.
func TestScanStoredIsOffWhenTheDomainToggleIsOff(t *testing.T) {
	found := fakeClamd(t, "stream: Eicar-Test-Signature FOUND\x00")
	fd := &fakeAVDir{domainID: 7, known: true} // both toggles off
	storedScanner(t, found, fd)

	if ScanStored(directory.StaticAccounts{}, "victim@acme.test", "x", []byte("MZ"), time.Unix(1000, 0)) {
		t.Error("content was scanned with the domain's toggle off")
	}
}

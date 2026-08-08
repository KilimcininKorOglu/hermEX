package ews

import (
	"encoding/base64"
	"strings"
	"testing"

	"hermex/internal/antivirus"
	"hermex/internal/avtest"
	"hermex/internal/directory"
	"hermex/internal/mta"
)

// scanDir is the directory capability the scanner gates on: one domain with
// inbound scanning enabled.
type scanDir struct{}

func (scanDir) GetDomainAVScan(string) (bool, bool, error)   { return true, false, nil }
func (scanDir) DomainID(string) (int64, bool, error)         { return 7, true, nil }
func (scanDir) DomainOrgAdminEmails(int64) ([]string, error) { return nil, nil }
func (scanDir) QuarantineMessage(e directory.QuarantineEntry) (int64, error) {
	return 1, nil
}

// withScanner points the package-level scanner at a stub clamd for one test.
func withScanner(t *testing.T, verdict string) {
	t.Helper()
	sc, err := antivirus.New(avtest.Clamd(t, verdict))
	if err != nil {
		t.Fatal(err)
	}
	quar := t.TempDir()
	mta.SetScanner(sc, scanDir{}, func(int64) string { return quar + "/q.eml" }, "mail.hermex.test", nil)
	t.Cleanup(func() { mta.SetScanner(nil, nil, nil, "", nil) })
}

// TestCreateAttachmentRefusesInfectedContent proves an attachment uploaded over
// EWS is scanned. These bytes never pass through delivery, so without the scan an
// authenticated user (or a delegate with edit rights on someone else's item)
// could park malware in a mailbox and wait for it to be opened.
func TestCreateAttachmentRefusesInfectedContent(t *testing.T) {
	withScanner(t, avtest.Infected)
	ts, _ := seededWithMessage(t, plainMessage)
	parent := parentItemID(t, ts)
	payload := base64.StdEncoding.EncodeToString([]byte("MZ malware bytes"))

	resp, out := soapPost(t, ts, createAttachmentReq(parent, "invoice.exe", "application/octet-stream", payload), true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, out)
	}
	if strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("infected attachment was accepted:\n%s", out)
	}
	if attachIDRE.MatchString(out) {
		t.Errorf("an attachment id was returned, so something was stored:\n%s", out)
	}
}

// TestCreateAttachmentAcceptsCleanContent keeps the ordinary path working with
// the scanner enabled.
func TestCreateAttachmentAcceptsCleanContent(t *testing.T) {
	withScanner(t, avtest.Clean)
	ts, _ := seededWithMessage(t, plainMessage)
	parent := parentItemID(t, ts)
	payload := base64.StdEncoding.EncodeToString([]byte("hello attach"))

	_, out := soapPost(t, ts, createAttachmentReq(parent, "note.txt", "text/plain", payload), true)
	if !strings.Contains(out, `ResponseClass="Success"`) {
		t.Errorf("clean attachment was refused:\n%s", out)
	}
}

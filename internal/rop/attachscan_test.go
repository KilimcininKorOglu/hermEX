package rop

import (
	"testing"

	"hermex/internal/antivirus"
	"hermex/internal/avtest"
	"hermex/internal/directory"
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/mta"
)

// scanDir is the directory capability the scanner gates on: one domain with
// inbound scanning enabled.
type scanDir struct{}

func (scanDir) GetDomainAVScan(string) (bool, bool, error)   { return true, false, nil }
func (scanDir) DomainID(string) (int64, bool, error)         { return 7, true, nil }
func (scanDir) DomainOrgAdminEmails(int64) ([]string, error) { return nil, nil }
func (scanDir) QuarantineMessage(directory.QuarantineEntry) (int64, error) {
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
	mta.SetScanner(sc, scanDir{}, func(int64) string { return quar + "/q.eml" }, "mail.test", nil)
	t.Cleanup(func() { mta.SetScanner(nil, nil, nil, "", nil) })
}

// saveAttachmentWith writes the given attachment properties and saves, returning
// the ROP return value.
func saveAttachmentWith(t *testing.T, sess *Session, msgH, attH uint32, props mapi.PropertyValues) uint32 {
	t.Helper()
	sess.Dispatch(buildSetProperties(0, props), []uint32{attH})
	out, _ := sess.Dispatch(buildSaveChangesAttachment(0, 1), []uint32{msgH, attH})
	p := ext.NewPull(out, ext.FlagUTF16)
	mustU8(t, p, "RopId")
	mustU8(t, p, "hindex")
	return mustU32(t, p, "ec")
}

// TestSaveChangesAttachmentRefusesInfectedContent proves a MAPI client's
// attachment bytes are scanned. They are written straight into the mailbox and
// never pass through delivery, so this is the only point they can be checked.
func TestSaveChangesAttachmentRefusesInfectedContent(t *testing.T) {
	withScanner(t, avtest.Infected)
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "HOST"))

	sess := NewSession(dir, nil, "alice@hermex.test")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	_, attH := createAttachmentNum(t, sess, msgH)

	ec := saveAttachmentWith(t, sess, msgH, attH, mapi.PropertyValues{
		{Tag: mapi.PrAttachLongFilename, Value: "invoice.exe"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("MZ malware bytes")},
	})
	if ec == ecSuccess {
		t.Error("an infected attachment was saved into the mailbox")
	}
}

// TestSaveChangesAttachmentAcceptsCleanContent keeps ordinary attachment writes
// working with the scanner enabled.
func TestSaveChangesAttachmentAcceptsCleanContent(t *testing.T) {
	withScanner(t, avtest.Clean)
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "HOST"))

	sess := NewSession(dir, nil, "alice@hermex.test")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	_, attH := createAttachmentNum(t, sess, msgH)

	ec := saveAttachmentWith(t, sess, msgH, attH, mapi.PropertyValues{
		{Tag: mapi.PrAttachLongFilename, Value: "notes.txt"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("harmless")},
	})
	if ec != ecSuccess {
		t.Errorf("a clean attachment was refused: %#x", ec)
	}
}

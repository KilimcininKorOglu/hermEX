package rop

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestAttachedContactIsSentAsAVCard composes a contact inside a message, the way
// Outlook attaches one, and proves the contact survives what storing it as RFC 5322
// bytes would lose: the parent is sent with a vCard 3.0 part ([MS-OXCMAIL]
// 2.1.3.4.6), and the attachment reopens with its contact class and fields.
func TestAttachedContactIsSentAsAVCard(t *testing.T) {
	dir := t.TempDir()
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	mid := uint64(seedInboxMessage(t, dir, "CARRIER"))
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	store := sess.get(logonH).store
	ids, err := store.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameEmail1Address})
	if err != nil {
		t.Fatal(err)
	}
	emailTag := mapi.MakeTag(ids[0], mapi.PtUnicode)

	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	msgH := h[1]
	_, attH := createAttachmentNum(t, sess, msgH)
	_, h = sess.Dispatch(buildOpenEmbeddedMessage(0, 1, mapiCreate), []uint32{attH, 0xFFFFFFFF})
	embH := h[1]
	sess.Dispatch(buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
		{Tag: mapi.PrDisplayName, Value: "Ada Lovelace"},
		{Tag: emailTag, Value: "ada@example.org"},
	}), []uint32{embH})
	ropOK(t, dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, embH), ropSaveChangesMessage, "SaveChangesMessage(contact)")
	ropOK(t, dispatchOn(sess, buildSaveChangesAttachment(0, 1), msgH, attH), ropSaveChangesAttachment, "SaveChangesAttachment")
	saveChangesEID(t, dispatchOn(sess, buildSaveChangesMessage(0, 1), logonH, msgH))

	saved, err := store.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := oxcmail.Export(saved, store.ExportOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Content-Type: text/directory; profile="vCard"; charset=utf-8`,
		"Content-Transfer-Encoding: quoted-printable", "VERSION:3.0", "FN:Ada Lovelace", "EMAIL:ada@example.org"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("sent message lacks %q:\n%s", want, raw)
		}
	}

	_, h = sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, mid))), []uint32{logonH, 0xFFFFFFFF})
	_, h = sess.Dispatch(buildOpenAttachment(0, 1, 0), []uint32{h[1], 0xFFFFFFFF})
	_, h = sess.Dispatch(buildOpenEmbeddedMessage(0, 1, mapiModify), []uint32{h[1], 0xFFFFFFFF})
	cols := []mapi.PropTag{mapi.PrMessageClass, emailTag}
	p := ropOK(t, dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), h[1]), ropGetPropertiesSpecific, "GetPropertiesSpecific(contact)")
	row := decodeRow(t, p, cols)
	wantProp(t, row, mapi.PrMessageClass, "IPM.Contact", "reopened class")
	wantProp(t, row, emailTag, "ada@example.org", "reopened email")
}

// dispatchOn runs one request over the given handles and returns its response.
func dispatchOn(sess *Session, req []byte, handles ...uint32) []byte {
	out, _ := sess.Dispatch(req, handles)
	return out
}

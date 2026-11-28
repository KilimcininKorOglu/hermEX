package rop

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// attachedContact is a contact composed inside a message the way Outlook attaches
// one: the carrier message, its session and logon, and the named email property tag.
type attachedContact struct {
	sess     *Session
	logonH   uint32
	store    *objectstore.Store
	inboxEID uint64
	mid      uint64
	emailTag mapi.PropTag
}

// composeAttachedContact opens a seeded Inbox message, attaches a new embedded
// message carrying the given contact fields, and saves the attachment and the
// carrier. Every step must succeed, so a later assertion never reads a half-built
// message.
func composeAttachedContact(t *testing.T, displayName, email string) attachedContact {
	t.Helper()
	dir := provisionedDir(t)
	c := attachedContact{
		inboxEID: uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox)),
		mid:      uint64(seedInboxMessage(t, dir, "CARRIER")),
		sess:     NewSession(dir, nil, ""),
	}
	t.Cleanup(c.sess.Close)
	c.logonH = dispatchOK(t, c.sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	c.store = c.sess.get(c.logonH).store
	ids, err := c.store.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameEmail1Address})
	if err != nil {
		t.Fatal(err)
	}
	c.emailTag = mapi.MakeTag(ids[0], mapi.PtUnicode)

	h := dispatchOK(t, c.sess, buildOpenMessage(0, 1, c.inboxEID, uint64(mapi.MakeEIDEx(1, c.mid))), ropOpenMessage, "OpenMessage", c.logonH, 0xFFFFFFFF)
	msgH := h[1]
	// The carrier is seeded without attachments, so the first one is number 0
	// ([MS-OXCMSG] 2.2.3.13.2: AttachmentID numbers attachments within the message).
	num, attH := createAttachmentNum(t, c.sess, msgH)
	if num != 0 {
		t.Fatalf("first AttachmentID = %d, want 0", num)
	}
	h = dispatchOK(t, c.sess, buildOpenEmbeddedMessage(0, 1, mapiCreate), ropOpenEmbeddedMessage, "OpenEmbeddedMessage(create)", attH, 0xFFFFFFFF)
	embH := h[1]
	dispatchOK(t, c.sess, buildSetProperties(0, mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
		{Tag: mapi.PrDisplayName, Value: displayName},
		{Tag: c.emailTag, Value: email},
	}), ropSetProperties, "SetProperties(contact)", embH)
	ropOK(t, dispatchOn(c.sess, buildSaveChangesMessage(0, 1), c.logonH, embH), ropSaveChangesMessage, "SaveChangesMessage(contact)")
	ropOK(t, dispatchOn(c.sess, buildSaveChangesAttachment(0, 1), msgH, attH), ropSaveChangesAttachment, "SaveChangesAttachment")
	saveChangesEID(t, dispatchOn(c.sess, buildSaveChangesMessage(0, 1), c.logonH, msgH))
	return c
}

// exported renders the saved carrier the way it is sent.
func (c attachedContact) exported(t *testing.T) string {
	t.Helper()
	saved, err := c.store.OpenMessage(int64(c.mid))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := oxcmail.Export(saved, c.store.ExportOptions())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestAttachedContactIsSentAsAVCard composes a contact inside a message, the way
// Outlook attaches one, and proves the contact survives what storing it as RFC 5322
// bytes would lose: the parent is sent with a vCard 3.0 part ([MS-OXCMAIL]
// 2.1.3.4.6), and the attachment reopens with its contact class and fields.
func TestAttachedContactIsSentAsAVCard(t *testing.T) {
	c := composeAttachedContact(t, "Ada Lovelace", "ada@example.org")
	raw := c.exported(t)
	for _, want := range []string{`Content-Type: text/directory; profile="vCard"; charset=utf-8`,
		"Content-Transfer-Encoding: quoted-printable", "VERSION:3.0", "FN:Ada Lovelace", "EMAIL:ada@example.org"} {
		if !strings.Contains(raw, want) {
			t.Errorf("sent message lacks %q:\n%s", want, raw)
		}
	}

	h := dispatchOK(t, c.sess, buildOpenMessage(0, 1, c.inboxEID, uint64(mapi.MakeEIDEx(1, c.mid))), ropOpenMessage, "OpenMessage(reopen)", c.logonH, 0xFFFFFFFF)
	h = dispatchOK(t, c.sess, buildOpenAttachment(0, 1, 0), ropOpenAttachment, "OpenAttachment", h[1], 0xFFFFFFFF)
	h = dispatchOK(t, c.sess, buildOpenEmbeddedMessage(0, 1, mapiModify), ropOpenEmbeddedMessage, "OpenEmbeddedMessage(modify)", h[1], 0xFFFFFFFF)
	cols := []mapi.PropTag{mapi.PrMessageClass, c.emailTag}
	p := ropOK(t, dispatchOn(c.sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), h[1]), ropGetPropertiesSpecific, "GetPropertiesSpecific(contact)")
	row := decodeRow(t, p, cols)
	wantProp(t, row, mapi.PrMessageClass, "IPM.Contact", "reopened class")
	wantProp(t, row, c.emailTag, "ada@example.org", "reopened email")
}

// TestAttachedContactLineBreakCannotAddAProperty sets a contact display name that
// carries a CRLF and a second vCard line. A vCard text value escapes a line break
// as "\n" (RFC 2426 section 4), so the client-supplied name must not become an
// EMAIL property of its own in the card the recipient imports.
func TestAttachedContactLineBreakCannotAddAProperty(t *testing.T) {
	c := composeAttachedContact(t, "Ada\r\nEMAIL:attacker@example.net", "ada@example.org")
	raw := c.exported(t)
	if !strings.Contains(raw, "VERSION:3.0") {
		t.Fatalf("sent message carries no vCard:\n%s", raw)
	}
	for line := range strings.SplitSeq(strings.ReplaceAll(raw, "=\r\n", ""), "\r\n") {
		if strings.HasPrefix(line, "EMAIL:attacker@example.net") || strings.HasPrefix(line, "EMAIL=3Aattacker") {
			t.Fatalf("the display name injected a vCard line %q:\n%s", line, raw)
		}
	}
}

package rop

import (
	"bytes"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// Wire values the copy tests expect, spelled out from the specification rather
// than taken from the production constants, so a wrong constant cannot pass.
const (
	// RopId of RopCopyProperties and RopCopyTo ([MS-OXCROPS] 2.2.8.11.2, 2.2.8.12.2).
	specRopCopyProperties uint8 = 0x67
	specRopCopyTo         uint8 = 0x39
	// ReturnValue of the null destination failure response ([MS-OXCROPS] 2.2.8.11.3,
	// 2.2.8.12.3).
	specNullDestination uint32 = 0x00000503
	// NotSupported ([MS-OXCDATA] 2.4), the code [MS-OXCPRPT] 3.2.5.8 gives a copy
	// between objects that are not of the same type.
	specNotSupported uint32 = 0x80040102
	// ecError ([MS-OXCDATA] 2.4): the package answers a source handle the session
	// never issued with it, as it does for every other ROP; the specification
	// names no code for that case.
	specGenericError uint32 = 0x80004005
)

// buildCopyProperties builds a RopCopyProperties request: source at srcIdx,
// destination at dstIdx (DestHandleIndex), the copy flags, and the inclusive tag set.
func buildCopyProperties(srcIdx, dstIdx, copyFlags uint8, tags []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCopyProperties)
	b.Uint8(0) // LogonId
	b.Uint8(srcIdx)
	b.Uint8(dstIdx)
	b.Uint8(0) // WantAsynchronous
	b.Uint8(copyFlags)
	mustBuild(b.PropTags(tags))
	return b.Bytes()
}

// buildCopyTo builds a RopCopyTo request: source at srcIdx, destination at dstIdx,
// the copy flags, and the excluded tag set.
func buildCopyTo(srcIdx, dstIdx, copyFlags uint8, excluded []mapi.PropTag) []byte {
	return buildCopyToWS(srcIdx, dstIdx, 0, copyFlags, excluded)
}

// copyFixture is a logged-on session over a mailbox holding one Inbox message.
type copyFixture struct {
	sess     *Session
	logonH   uint32
	store    *objectstore.Store
	inboxEID uint64
	mid      uint64
}

// newCopyFixture provisions a mailbox, seeds one Inbox message with subject, and
// logs a session on to it.
func newCopyFixture(t *testing.T, subject string) *copyFixture {
	t.Helper()
	dir := provisionedDir(t)
	mid := uint64(seedInboxMessage(t, dir, subject))
	sess := NewSession(dir, nil, "")
	t.Cleanup(sess.Close)
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	return &copyFixture{
		sess:     sess,
		logonH:   h[0],
		store:    sess.get(h[0]).store,
		inboxEID: uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox)),
		mid:      mid,
	}
}

// openSource opens the seeded message and returns its handle.
func (f *copyFixture) openSource(t *testing.T) uint32 {
	t.Helper()
	h := dispatchOK(t, f.sess, buildOpenMessage(0, 1, f.inboxEID, uint64(mapi.MakeEIDEx(1, f.mid))),
		ropOpenMessage, "OpenMessage", f.logonH, 0xFFFFFFFF)
	return h[1]
}

// newCompose creates a compose message in the Inbox and returns its handle.
func (f *copyFixture) newCompose(t *testing.T) uint32 {
	t.Helper()
	h := dispatchOK(t, f.sess, buildCreateMessage(0, 1, f.inboxEID), ropCreateMessage, "CreateMessage", f.logonH, 0xFFFFFFFF)
	return h[1]
}

// save saves the message at msgH and returns its stored message id.
func (f *copyFixture) save(t *testing.T, msgH uint32) int64 {
	t.Helper()
	resp := mustDispatch(f.sess, buildSaveChangesMessage(0, 1), f.logonH, msgH)
	return int64(mapi.EID(saveChangesEID(t, resp)).GCValue())
}

// copyHeader reads a copy response's RopId, SourceHandleIndex and ReturnValue.
// Every request in these tests addresses the source at slot 0, which the
// response MUST echo ([MS-OXCROPS] 2.2.8.11.2, 2.2.8.12.2).
func copyHeader(t *testing.T, resp []byte, wantRop uint8, label string) (*ext.Pull, uint32) {
	t.Helper()
	p := ext.NewPull(resp, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != wantRop {
		t.Fatalf("%s RopId = %#x, want %#x", label, id, wantRop)
	}
	if hi := mustU8(t, p, "SourceHandleIndex"); hi != 0 {
		t.Fatalf("%s SourceHandleIndex = %d, want 0", label, hi)
	}
	return p, mustU32(t, p, "ReturnValue")
}

// wantCopySuccess asserts a copy succeeded with an empty PropertyProblems array
// and nothing after it ([MS-OXCROPS] 2.2.8.11.2, 2.2.8.12.2).
func wantCopySuccess(t *testing.T, resp []byte, wantRop uint8, label string) {
	t.Helper()
	p, ec := copyHeader(t, resp, wantRop, label)
	if ec != ecSuccess {
		t.Fatalf("%s ReturnValue = %#x, want success", label, ec)
	}
	if pc := mustU16(t, p, "PropertyProblemCount"); pc != 0 {
		t.Errorf("%s PropertyProblemCount = %d, want 0", label, pc)
	}
	wantDrained(t, p, label)
}

// wantCopyFailure asserts a copy failed with want and carried no body
// ([MS-OXCROPS] 2.2.8.11.4, 2.2.8.12.4).
func wantCopyFailure(t *testing.T, resp []byte, wantRop uint8, want uint32, label string) {
	t.Helper()
	p, ec := copyHeader(t, resp, wantRop, label)
	if ec != want {
		t.Fatalf("%s ReturnValue = %#x, want %#x", label, ec, want)
	}
	wantDrained(t, p, label)
}

// wantNoSubject asserts the in-memory bag of the object at h carries no subject.
func wantNoSubject(t *testing.T, sess *Session, h uint32, label string) {
	t.Helper()
	obj := sess.get(h)
	if obj == nil {
		t.Fatalf("%s: handle %d resolves to no object", label, h)
	}
	props, ok := sess.objectAllProps(obj)
	if !ok {
		t.Fatalf("%s: the object's properties could not be read", label)
	}
	if v, present := props.Get(mapi.PrSubject); present {
		t.Errorf("%s carries subject %v, want none", label, v)
	}
}

// TestCopyPropertiesAndCopyTo drives RopCopyProperties and RopCopyTo from an opened
// source message to fresh compose messages. CopyProperties copies only the listed
// tag; CopyTo copies every property except the excluded one ([MS-OXCPRPT] 3.2.5.7,
// 3.2.5.8). Both are verified by saving the destination and reading the store.
func TestCopyPropertiesAndCopyTo(t *testing.T) {
	f := newCopyFixture(t, "COPYSRC")

	// Source: open and add PrImportance so there are two scalar properties to copy.
	srcH := f.openSource(t)
	dispatchOK(t, f.sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrImportance, Value: int32(2)}}),
		ropSetProperties, "SetProperties", srcH)
	if srcID := f.save(t, srcH); uint64(srcID) != f.mid {
		t.Fatalf("saving the source stored message %d, want %d", srcID, f.mid)
	}

	// CopyProperties: copy only PrSubject into a fresh compose message.
	dstH := f.newCompose(t)
	wantCopySuccess(t, dispatchOn(f.sess, buildCopyProperties(0, 1, 0, []mapi.PropTag{mapi.PrSubject}), srcH, dstH),
		specRopCopyProperties, "CopyProperties")
	cpProps := mustMessageProps(t, f.store, f.save(t, dstH), mapi.PrSubject, mapi.PrImportance)
	wantProp(t, cpProps, mapi.PrSubject, "COPYSRC", "CopyProperties subject")
	if v, ok := cpProps.Get(mapi.PrImportance); ok {
		t.Errorf("CopyProperties copied the unlisted importance %v", v)
	}

	// CopyTo: copy everything except PrImportance into another compose message.
	dst2H := f.newCompose(t)
	wantCopySuccess(t, dispatchOn(f.sess, buildCopyTo(0, 1, 0, []mapi.PropTag{mapi.PrImportance}), srcH, dst2H),
		specRopCopyTo, "CopyTo")
	ctProps := mustMessageProps(t, f.store, f.save(t, dst2H), mapi.PrSubject, mapi.PrImportance)
	wantProp(t, ctProps, mapi.PrSubject, "COPYSRC", "CopyTo subject")
	if v, ok := ctProps.Get(mapi.PrImportance); ok {
		t.Errorf("CopyTo copied the excluded importance %v", v)
	}
}

// TestCopyRefusesANullDestination sends both copy ROPs with the null handle in the
// destination slot. Each answers the null destination failure response: ReturnValue
// 0x503 and the request's DestHandleIndex echoed as a 4-byte field ([MS-OXCROPS]
// 2.2.8.11.3, 2.2.8.12.3). The source is left as it was stored.
func TestCopyRefusesANullDestination(t *testing.T) {
	f := newCopyFixture(t, "COPYNULL")
	srcH := f.openSource(t)

	for _, c := range []struct {
		label string
		rop   uint8
		req   []byte
	}{
		{"null-destination CopyProperties", specRopCopyProperties, buildCopyProperties(0, 1, 0, []mapi.PropTag{mapi.PrSubject})},
		{"null-destination CopyTo", specRopCopyTo, buildCopyTo(0, 1, 0, nil)},
	} {
		p, ec := copyHeader(t, dispatchOn(f.sess, c.req, srcH, 0xFFFFFFFF), c.rop, c.label)
		if ec != specNullDestination {
			t.Fatalf("%s ReturnValue = %#x, want %#x", c.label, ec, specNullDestination)
		}
		if dh := mustU32(t, p, "DestHandleIndex"); dh != 1 {
			t.Errorf("%s DestHandleIndex = %d, want the request's 1", c.label, dh)
		}
		wantDrained(t, p, c.label)
	}

	wantProp(t, mustMessageProps(t, f.store, int64(f.mid), mapi.PrSubject), mapi.PrSubject, "COPYNULL", "stored source subject")
}

// TestCopyRefusesMismatchedObjects copies a message onto an attachment. Source and
// destination must be of the same type, otherwise the copy answers NotSupported
// ([MS-OXCPRPT] 3.2.5.8, RopCopyTo). RopCopyProperties answers the same code by
// project decision, since 3.2.5.7 lists no codes. The refused copies write nothing
// onto the attachment.
func TestCopyRefusesMismatchedObjects(t *testing.T) {
	f := newCopyFixture(t, "COPYERR")
	srcH := f.openSource(t)
	num, attH := createAttachmentNum(t, f.sess, srcH)
	if att := f.sess.get(attH); att == nil || att.attachW == nil || att.attachW.attachNum != num {
		t.Fatalf("CreateAttachment handle %d does not hold attachment %d", attH, num)
	}

	wantCopyFailure(t, dispatchOn(f.sess, buildCopyTo(0, 1, 0, nil), srcH, attH),
		specRopCopyTo, specNotSupported, "message-to-attachment CopyTo")
	wantCopyFailure(t, dispatchOn(f.sess, buildCopyProperties(0, 1, 0, []mapi.PropTag{mapi.PrSubject}), srcH, attH),
		specRopCopyProperties, specNotSupported, "message-to-attachment CopyProperties")

	wantNoSubject(t, f.sess, attH, "the attachment after the refused copies")
}

// TestCopyRefusesASourceNeverIssued sends both copy ROPs with a source handle the
// session never issued. Each fails and the compose destination stays empty.
func TestCopyRefusesASourceNeverIssued(t *testing.T) {
	f := newCopyFixture(t, "COPYBOGUS")
	dstH := f.newCompose(t)
	bogus := dstH + 1000

	wantCopyFailure(t, dispatchOn(f.sess, buildCopyProperties(0, 1, 0, []mapi.PropTag{mapi.PrSubject}), bogus, dstH),
		specRopCopyProperties, specGenericError, "CopyProperties from a source never issued")
	wantCopyFailure(t, dispatchOn(f.sess, buildCopyTo(0, 1, 0, nil), bogus, dstH),
		specRopCopyTo, specGenericError, "CopyTo from a source never issued")

	if f.sess.get(bogus) != nil {
		t.Errorf("the refused copies registered an object at handle %d", bogus)
	}
	wantNoSubject(t, f.sess, dstH, "the compose destination after the refused copies")
}

// TestCopyReflectsBufferedEdits proves CopyTo copies the source's open working
// copy: a property set on the source but not yet saved is carried into the
// destination. Reading the source from the store alone would copy the stale value.
func TestCopyReflectsBufferedEdits(t *testing.T) {
	f := newCopyFixture(t, "COPYBUF")

	// The seeded message is stored with importance 1 (Normal). Open it and set
	// importance to 2 (High, [MS-OXCMSG] 2.2.1.11) without saving: the store still
	// holds 1, only the open working copy holds 2.
	srcH := f.openSource(t)
	dispatchOK(t, f.sess, buildSetProperties(0, mapi.PropertyValues{{Tag: mapi.PrImportance, Value: int32(2)}}),
		ropSetProperties, "SetProperties", srcH)
	wantProp(t, mustMessageProps(t, f.store, int64(f.mid), mapi.PrImportance), mapi.PrImportance, int32(1),
		"stored source importance before its save")

	// CopyTo into a fresh compose message, excluding nothing, then save it.
	dstH := f.newCompose(t)
	wantCopySuccess(t, dispatchOn(f.sess, buildCopyTo(0, 1, 0, nil), srcH, dstH), specRopCopyTo, "CopyTo")
	props := mustMessageProps(t, f.store, f.save(t, dstH), mapi.PrImportance)
	wantProp(t, props, mapi.PrImportance, int32(2), "CopyTo importance from the source's buffered edit")
}

// buildCopyToWS builds a RopCopyTo request with an explicit WantSubObjects byte,
// for the sub-object copy tests.
func buildCopyToWS(srcIdx, dstIdx, wantSub, copyFlags uint8, excluded []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropCopyTo)
	b.Uint8(0) // LogonId
	b.Uint8(srcIdx)
	b.Uint8(dstIdx)
	b.Uint8(0) // WantAsynchronous
	b.Uint8(wantSub)
	b.Uint8(copyFlags)
	mustBuild(b.PropTags(excluded))
	return b.Bytes()
}

// seedMessageWithSubObjects writes an Inbox message carrying one recipient and one
// by-value attachment (a forward scenario), so a copy of it has sub-objects to
// carry. Returns the stored message id.
func seedMessageWithSubObjects(t *testing.T, dir string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	msg := &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
			{Tag: mapi.PrSubject, Value: "FWDSRC"},
		},
		Recipients: []mapi.PropertyValues{{
			{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
			{Tag: mapi.PrDisplayName, Value: "Bob"},
			{Tag: mapi.PrEmailAddress, Value: "bob@hermex.test"},
			{Tag: mapi.PrAddrType, Value: "SMTP"},
			{Tag: mapi.PrSmtpAddress, Value: "bob@hermex.test"},
		}},
		Attachments: []oxcmail.Attachment{{Props: mapi.PropertyValues{
			{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
			{Tag: mapi.PrAttachLongFilename, Value: "note.txt"},
			{Tag: mapi.PrAttachDataBin, Value: []byte("attached-bytes")},
		}}},
	}
	id, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), msg)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestCopyToSubObjects proves RopCopyTo carries a message's sub-objects: with
// WantSubObjects set ([MS-OXCPRPT] 2.2.11.1), the recipient and the attachment
// (including its data) are copied into the compose destination; excluding
// PR_MESSAGE_ATTACHMENTS suppresses the attachment while keeping the recipient;
// and without WantSubObjects neither sub-object is copied, only scalar properties.
func TestCopyToSubObjects(t *testing.T) {
	dir := provisionedDir(t)
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	srcID := uint64(seedMessageWithSubObjects(t, dir))

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	store := sess.get(logonH).store

	// Open the source, CopyTo into a fresh compose message, save it, and return the
	// stored destination message.
	copyInto := func(wantSub uint8, excluded []mapi.PropTag) *oxcmail.Message {
		t.Helper()
		sH := dispatchOK(t, sess, buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, srcID))),
			ropOpenMessage, "OpenMessage", logonH, 0xFFFFFFFF)[1]
		dH := dispatchOK(t, sess, buildCreateMessage(0, 1, inboxEID), ropCreateMessage, "CreateMessage", logonH, 0xFFFFFFFF)[1]
		wantCopySuccess(t, dispatchOn(sess, buildCopyToWS(0, 1, wantSub, 0, excluded), sH, dH), specRopCopyTo, "CopyTo")
		dstID := int64(mapi.EID(saveChangesEID(t, mustDispatch(sess, buildSaveChangesMessage(0, 1), logonH, dH))).GCValue())
		m, err := store.OpenMessage(dstID)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	// WantSubObjects: recipient and attachment (with data) both copied.
	full := copyInto(1, nil)
	wantSubObjectCounts(t, full, 1, 1, "WantSubObjects")
	wantProp(t, full.Recipients[0], mapi.PrSmtpAddress, "bob@hermex.test", "copied recipient smtp")
	data, ok := full.Attachments[0].Props.Get(mapi.PrAttachDataBin)
	if !ok || !bytes.Equal(asBytes(data), []byte("attached-bytes")) {
		t.Errorf("copied attachment data = %q (present=%v), want attached-bytes", asBytes(data), ok)
	}

	// Exclude PR_MESSAGE_ATTACHMENTS: recipient still copied, attachment suppressed.
	noAtt := copyInto(1, []mapi.PropTag{mapi.PrMessageAttachments})
	wantSubObjectCounts(t, noAtt, 1, 0, "attachments excluded")

	// WantSubObjects FALSE: no sub-objects, but scalar properties still copied.
	none := copyInto(0, nil)
	wantSubObjectCounts(t, none, 0, 0, "without WantSubObjects")
	wantProp(t, none.Props, mapi.PrSubject, "FWDSRC", "scalar subject")
}

// wantSubObjectCounts asserts how many recipients and attachments a copy carried.
func wantSubObjectCounts(t *testing.T, m *oxcmail.Message, recips, attachs int, what string) {
	t.Helper()
	if len(m.Recipients) != recips || len(m.Attachments) != attachs {
		t.Fatalf("%s: copied recips=%d attachs=%d, want recips=%d attachs=%d",
			what, len(m.Recipients), len(m.Attachments), recips, attachs)
	}
}

// mustMessageProps reads tags from a stored message and fails the test when the
// read itself fails, so an absence check never passes on an empty result.
func mustMessageProps(t *testing.T, store *objectstore.Store, id int64, tags ...mapi.PropTag) mapi.PropertyValues {
	t.Helper()
	props, err := store.GetMessageProperties(id, tags...)
	if err != nil {
		t.Fatal(err)
	}
	return props
}

// asBytes coerces a property value to its byte slice (PtBinary), or nil for any
// other type.
func asBytes(v any) []byte {
	if b, ok := v.([]byte); ok {
		return b
	}
	return nil
}

// mustDispatch dispatches a single ROP with a two-slot handle array and returns the
// response bytes, a small helper for the save steps in the copy tests.
func mustDispatch(sess *Session, rop []byte, h0, h1 uint32) []byte {
	return dispatchOn(sess, rop, h0, h1)
}

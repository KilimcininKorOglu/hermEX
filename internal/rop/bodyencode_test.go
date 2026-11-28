package rop

import (
	"errors"
	"strings"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// oversizedRecipient is a stored recipient whose RECIPIENT_ROW cannot be framed:
// RecipientRowSize is a 16-bit field ([MS-OXCMSG] 2.2.3.1.2), and a display name of
// 0x8000 UTF-16 code units alone is 0x10002 bytes with its terminator. A stored
// message is not bound by that limit (an ICS import or a delivered header can
// carry such a name), so the response must refuse it rather than truncate the size.
func oversizedRecipient() mapi.PropertyValues {
	return mapi.PropertyValues{
		{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
		{Tag: mapi.PrDisplayName, Value: strings.Repeat("a", 0x8000)},
		{Tag: mapi.PrAddrType, Value: "SMTP"},
		{Tag: mapi.PrEmailAddress, Value: "big@example.com"},
	}
}

// TestRecipientRowOverFieldWidthRefused proves a recipient row longer than its
// 16-bit size field reports an error instead of writing a wrapped size that would
// make the client misparse every byte after it.
func TestRecipientRowOverFieldWidthRefused(t *testing.T) {
	out := ext.NewPush(ext.FlagUTF16)
	err := writeRecipientTable(out, []mapi.PropertyValues{oversizedRecipient()})
	if !errors.Is(err, ext.ErrFormat) {
		t.Fatalf("writeRecipientTable(oversized row) = %v, want ext.ErrFormat", err)
	}
}

// TestWriteBodyFailureAnswersOnlyTheError proves a response whose body does not
// encode is the bare six-byte header with a failing ReturnValue ([MS-OXCROPS]
// 2.2.4.2: a failure response carries RopId, HandleIndex and ReturnValue only),
// with none of the partly written body after it.
func TestWriteBodyFailureAnswersOnlyTheError(t *testing.T) {
	out := ext.NewPush(ext.FlagUTF16)
	writeBody(out, ropReadStream, 3, func(b *ext.Push) error {
		b.Uint32(0xDEADBEEF) // written before the failure; must not reach the client
		return ext.ErrFormat
	})
	b := out.Bytes()
	if len(b) != 6 {
		t.Fatalf("response is %d bytes (% x), want the 6-byte failure header", len(b), b)
	}
	p := ext.NewPull(b, ext.FlagUTF16)
	wantU8(t, p, "RopId", ropReadStream)
	wantU8(t, p, "HandleIndex", 3)
	if ec := mustU32(t, p, "ReturnValue"); ec == ecSuccess {
		t.Error("ReturnValue = ecSuccess for a body that did not encode")
	}
}

// TestOpenMessageUnencodableRecipientAllocatesNoHandle opens a stored message whose
// recipient table cannot be framed. The client must get only a failure header, and
// the session must not hold an open message handle the client was never told about.
func TestOpenMessageUnencodableRecipientAllocatesNoHandle(t *testing.T) {
	dir := provisionedDir(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
			{Tag: mapi.PrSubject, Value: "oversized recipient"},
		},
		Recipients: []mapi.PropertyValues{oversizedRecipient()},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]
	before := len(sess.handles)

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	resp, h := sess.Dispatch(buildOpenMessage(0, 1, inboxEID, uint64(mapi.MakeEIDEx(1, uint64(mid)))), []uint32{logonH, 0xFFFFFFFF})
	if len(resp) != 6 {
		t.Fatalf("OpenMessage response is %d bytes, want the 6-byte failure header", len(resp))
	}
	p := ext.NewPull(resp, ext.FlagUTF16)
	wantU8(t, p, "RopId", ropOpenMessage)
	wantU8(t, p, "OutputHandleIndex", 1)
	if ec := mustU32(t, p, "ReturnValue"); ec == ecSuccess {
		t.Error("OpenMessage answered ecSuccess for a recipient table that did not encode")
	}
	if h[1] != 0xFFFFFFFF {
		t.Errorf("output handle slot = %#x, want it left unset", h[1])
	}
	if got := len(sess.handles); got != before {
		t.Errorf("session holds %d handles after the refused open, want %d", got, before)
	}
}

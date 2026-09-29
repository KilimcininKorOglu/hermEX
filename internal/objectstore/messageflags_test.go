package objectstore

import (
	"slices"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// computedMessage returns one computed message property.
func computedMessage(t *testing.T, s *Store, id int64, tag mapi.PropTag) any {
	t.Helper()
	props, err := s.MessageComputedProps(id, tag)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := props.Get(tag)
	return v
}

// TestMessageComputedProps proves a message reports its read state, its paperclip,
// its FAI bit and its pending receipts as they are now ([MS-OXCMSG] 2.2.1.6): a
// read made on another protocol shows, an inline picture is not a paperclip, and a
// stale derived bit a client once stored does not survive, while a bit the store
// does not derive does.
func TestMessageComputedProps(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	when := time.Unix(1700000000, 0)
	attached := mustAppendMessage(t, s, inbox, []byte("From: a@example.test\r\nSubject: att\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: multipart/mixed; boundary=B\r\n\r\n"+
		"--B\r\nContent-Type: text/plain\r\n\r\nhi\r\n"+
		"--B\r\nContent-Type: application/pdf; name=\"r.pdf\"\r\n"+
		"Content-Disposition: attachment; filename=\"r.pdf\"\r\n\r\n%PDF data\r\n--B--\r\n"), when, 0).ID
	inline := mustAppendMessage(t, s, inbox, []byte("From: a@example.test\r\nSubject: inline\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: multipart/related; boundary=B\r\n\r\n"+
		"--B\r\nContent-Type: text/html\r\n\r\n<img src=\"cid:x\">\r\n"+
		"--B\r\nContent-Type: image/png\r\nContent-ID: <x>\r\nContent-Disposition: inline\r\n\r\nPNGDATA\r\n--B--\r\n"), when, 0).ID
	const fromMe = 0x20
	stale := createInFolder(t, s, inbox, mapi.PropertyValues{
		{Tag: mapi.PrMessageFlags, Value: int32(fromMe | mapi.MsgFlagHasAttach)},
		{Tag: mapi.PrReadReceiptRequested, Value: true},
		{Tag: mapi.PrNonReceiptNotificationRequested, Value: true},
	})
	fai := createInFolder(t, s, inbox, mapi.PropertyValues{{Tag: mapi.PrAssociated, Value: true}})

	if err := s.SetMessageReadState(attached, true); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		id    int64
		flags int32
	}{
		{"read, attached", attached, mapi.MsgFlagRead | mapi.MsgFlagHasAttach},
		{"inline picture only", inline, 0},
		{"stale stored bits", stale, fromMe | mapi.MsgFlagRNPending | mapi.MsgFlagNRNPending},
		{"FAI", fai, mapi.MsgFlagAssociated},
	} {
		if v := computedMessage(t, s, c.id, mapi.PrMessageFlags); v != c.flags {
			t.Errorf("%s: flags = %#x, want %#x", c.name, v, c.flags)
		}
	}
	if v := computedMessage(t, s, attached, mapi.PrRead); v != true {
		t.Errorf("PidTagRead of a read message = %v", v)
	}
	if v := computedMessage(t, s, attached, mapi.PrHasAttachments); v != true {
		t.Errorf("PidTagHasAttachments of an attached message = %v", v)
	}
	if v := computedMessage(t, s, inline, mapi.PrHasAttachments); v != false {
		t.Errorf("PidTagHasAttachments of an inline-only message = %v", v)
	}
	if props, err := s.MessageComputedProps(attached, mapi.PrSubject); err != nil || len(props) != 0 {
		t.Errorf("a read asking for no computed tag = %v, %v; want none", props, err)
	}
}

// TestMessageComputedPropsBatch proves the batch read answers every message of a
// long id list, across chunks, as the single read does.
func TestMessageComputedPropsBatch(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	var ids []int64
	for i := range batchIDChunk(5) + 3 {
		props := mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "m"}}
		if i%2 == 0 {
			props.Set(mapi.PrMessageFlags, int32(mapi.MsgFlagRead))
		}
		ids = append(ids, createInFolder(t, s, inbox, props))
	}
	got, err := s.MessageComputedPropsBatch(ids, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		v, _ := got[id].Get(mapi.PrRead)
		if v != (i%2 == 0) {
			t.Fatalf("message %d read = %v, want %v", i, v, i%2 == 0)
		}
	}
}

// TestUnreadSearchFolder proves a search on the read bit of PidTagMessageFlags,
// Outlook's Unread Mail folder, holds the unread messages and drops one once it is
// read.
func TestUnreadSearchFolder(t *testing.T) {
	s, fid := searchStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	first := deliver(t, s, inbox, "first")
	second := deliver(t, s, inbox, "second")
	unread := mapi.Restriction{Type: mapi.ResBitmask, Value: mapi.BitmaskRestriction{
		Relop: mapi.BmrEqz, PropTag: mapi.PrMessageFlags, Mask: mapi.MsgFlagRead,
	}}
	mustNoErr(t, "set criteria", s.SetSearchCriteria(fid, SearchCriteria{Restriction: &unread, Scope: []int64{inbox}, Flags: mapi.SearchRestart}))
	if got := searchIDs(t, s, fid); !slices.Equal(got, []int64{first, second}) {
		t.Fatalf("results = %v, want [%d %d]", got, first, second)
	}
	mustNoErr(t, "mark read", s.SetMessageReadState(first, true))
	if got := searchIDs(t, s, fid); !slices.Equal(got, []int64{second}) {
		t.Fatalf("after read results = %v, want [%d]", got, second)
	}
}

// TestCopiedMessageKeepsItsReadState proves a FastTransfer copy carries the
// source's computed PidTagMessageFlags, so a read message copied through the
// stream is read at the destination too.
func TestCopiedMessageKeepsItsReadState(t *testing.T) {
	s := openSeededStore(t)
	id := createInFolder(t, s, int64(mapi.PrivateFIDInbox), mapi.PropertyValues{{Tag: mapi.PrSubject, Value: "copied"}})
	if err := s.SetMessageReadState(id, true); err != nil {
		t.Fatal(err)
	}
	msg, err := s.openTransferMessage(id)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := msg.Props.Get(mapi.PrMessageFlags); v != int32(mapi.MsgFlagRead) {
		t.Errorf("transferred flags = %v, want the read bit", v)
	}
}

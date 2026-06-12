package objectstore

import (
	"bytes"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// childIDs returns the ids produced by a single-column query, in query order.
func childIDs(t *testing.T, s *Store, query string, args ...any) []int64 {
	t.Helper()
	rows, err := s.objdb.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestCreateMessage inserts a full MAPI message object (envelope, large body,
// two recipients, one attachment) and verifies every part is persisted: the
// denormalized message row, the offloaded body, the recipient bags, the
// attachment with its offloaded payload, and the time-sort index entry.
func TestCreateMessage(t *testing.T) {
	s := openSeededStore(t)

	bodyText := bytes.Repeat([]byte("merhaba dünya — gövde\n"), 500)
	attachData := bytes.Repeat([]byte{0x25, 0x50, 0x44, 0x46}, 800)
	deliveredNT := mapi.UnixToNTTime(time.Unix(1700000000, 0))

	msg := &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
			{Tag: mapi.PrSubject, Value: "deneme konusu"},
			{Tag: mapi.PrBody, Value: string(bodyText)},
			{Tag: mapi.PrImportance, Value: int32(mapi.ImportanceHigh)},
			{Tag: mapi.PrMessageDeliveryTime, Value: deliveredNT},
		},
		Recipients: []mapi.PropertyValues{
			{
				{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
				{Tag: mapi.PrDisplayName, Value: "Alıcı Bir"},
				{Tag: mapi.PrSmtpAddress, Value: "bir@example.test"},
			},
			{
				{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipCc)},
				{Tag: mapi.PrDisplayName, Value: "Alıcı İki"},
				{Tag: mapi.PrSmtpAddress, Value: "iki@example.test"},
			},
		},
		Attachments: []oxcmail.Attachment{
			{Props: mapi.PropertyValues{
				{Tag: mapi.PrAttachLongFilename, Value: "rapor.pdf"},
				{Tag: mapi.PrAttachMimeTag, Value: "application/pdf"},
				{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
				{Tag: mapi.PrAttachDataBin, Value: attachData},
			}},
		},
	}

	eid, err := s.CreateMessage(mapi.PrivateFIDInbox, msg)
	if err != nil {
		t.Fatal(err)
	}
	if eid == 0 {
		t.Fatal("CreateMessage returned eid 0")
	}

	// get fails the test on a property-read error and returns the values.
	get := func(pv mapi.PropertyValues, err error) mapi.PropertyValues {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return pv
	}

	// Top-level properties, including the offloaded body, round-trip.
	gm := asMap(get(s.GetMessageProperties(eid)))
	if gm[mapi.PrSubject] != "deneme konusu" {
		t.Errorf("subject = %v", gm[mapi.PrSubject])
	}
	if gm[mapi.PrBody] != string(bodyText) {
		t.Error("body did not round-trip through the content offload")
	}
	if gm[mapi.PrImportance] != int32(mapi.ImportanceHigh) {
		t.Errorf("importance = %v", gm[mapi.PrImportance])
	}

	// The denormalized message row carries the hot columns.
	var (
		parentFID, msgSize int64
		mid                string
		readSt             int
	)
	if err := s.objdb.QueryRow(
		`SELECT parent_fid, message_size, mid_string, read_state FROM messages WHERE message_id=?`, eid).
		Scan(&parentFID, &msgSize, &mid, &readSt); err != nil {
		t.Fatal(err)
	}
	if parentFID != mapi.PrivateFIDInbox {
		t.Errorf("parent_fid = %d, want %d", parentFID, mapi.PrivateFIDInbox)
	}
	if mid != midString(uint64(eid)) {
		t.Errorf("mid_string = %q, want %q", mid, midString(uint64(eid)))
	}
	if msgSize <= int64(len(bodyText)) {
		t.Errorf("message_size = %d, want > body length %d", msgSize, len(bodyText))
	}
	if readSt != 0 {
		t.Errorf("read_state = %d, want 0 (delivered unread)", readSt)
	}

	// Two recipients, carrying their type and address.
	rids := childIDs(t, s, `SELECT recipient_id FROM recipients WHERE message_id=? ORDER BY recipient_id`, eid)
	if len(rids) != 2 {
		t.Fatalf("recipient count = %d, want 2", len(rids))
	}
	r0 := asMap(get(s.GetRecipientProperties(rids[0])))
	if r0[mapi.PrRecipientType] != int32(mapi.RecipTo) || r0[mapi.PrSmtpAddress] != "bir@example.test" {
		t.Errorf("recipient 0 = %#v", r0)
	}
	r1 := asMap(get(s.GetRecipientProperties(rids[1])))
	if r1[mapi.PrRecipientType] != int32(mapi.RecipCc) || r1[mapi.PrSmtpAddress] != "iki@example.test" {
		t.Errorf("recipient 1 = %#v", r1)
	}

	// One attachment, with its payload reloaded from the content file.
	aids := childIDs(t, s, `SELECT attachment_id FROM attachments WHERE message_id=? ORDER BY attachment_id`, eid)
	if len(aids) != 1 {
		t.Fatalf("attachment count = %d, want 1", len(aids))
	}
	ap := asMap(get(s.GetAttachmentProperties(aids[0])))
	if ap[mapi.PrAttachLongFilename] != "rapor.pdf" {
		t.Errorf("attachment filename = %v", ap[mapi.PrAttachLongFilename])
	}
	data, ok := ap[mapi.PrAttachDataBin].([]byte)
	if !ok || !bytes.Equal(data, attachData) {
		t.Error("attachment payload did not round-trip through the content offload")
	}

	// The time-sort index recorded the received (delivery) time.
	var rcv int64
	if err := s.objdb.QueryRow(`SELECT rcvtime FROM msgtime_index WHERE message_id=?`, eid).Scan(&rcv); err != nil {
		t.Fatal(err)
	}
	if uint64(rcv) != deliveredNT {
		t.Errorf("rcvtime = %d, want %d", rcv, deliveredNT)
	}
}

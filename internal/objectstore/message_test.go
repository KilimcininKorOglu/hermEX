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
	mustNoErr(t, "iterate ids", rows.Err())
	return ids
}

// TestCreateMessage inserts a full MAPI message object (envelope, large body,
// two recipients, one attachment) and verifies every part is persisted: the
// denormalized message row, the offloaded body, the recipient bags, the
// attachment with its offloaded payload, and the time-sort index entry.
func TestCreateMessage(t *testing.T) {
	s := openSeededStore(t)

	bodyText := bytes.Repeat([]byte("merhaba dünya, gövde\n"), 500)
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

	eid := mustCreateMessage(t, s, mapi.PrivateFIDInbox, msg)
	if eid == 0 {
		t.Fatal("CreateMessage returned eid 0")
	}

	// Top-level properties, including the offloaded body, round-trip.
	gm := messageProps(t, s, eid)
	wantEq(t, "subject", gm[mapi.PrSubject], any("deneme konusu"))
	wantEq(t, "body through the content offload", gm[mapi.PrBody], any(string(bodyText)))
	wantEq(t, "importance", gm[mapi.PrImportance], any(int32(mapi.ImportanceHigh)))

	wantMessageRow(t, s, eid, int64(len(bodyText)))
	wantRecipientBags(t, s, eid)
	wantAttachmentPayload(t, s, eid, attachData)

	// The time-sort index recorded the received (delivery) time.
	var rcv int64
	mustScan(t, s.objdb.QueryRow(`SELECT rcvtime FROM msgtime_index WHERE message_id=?`, eid), &rcv)
	wantEq(t, "rcvtime", uint64(rcv), deliveredNT)
}

// messageProps, recipientProps and attachmentProps read one object's property
// bag as a map, failing the test on a read error.
func messageProps(t *testing.T, s *Store, messageID int64, tags ...mapi.PropTag) map[mapi.PropTag]any {
	t.Helper()
	pv, err := s.GetMessageProperties(messageID, tags...)
	mustNoErr(t, "read message properties", err)
	return asMap(pv)
}

func folderProps(t *testing.T, s *Store, folderID int64, tags ...mapi.PropTag) map[mapi.PropTag]any {
	t.Helper()
	pv, err := s.GetFolderProperties(folderID, tags...)
	mustNoErr(t, "read folder properties", err)
	return asMap(pv)
}

func recipientProps(t *testing.T, s *Store, recipientID int64) map[mapi.PropTag]any {
	t.Helper()
	pv, err := s.GetRecipientProperties(recipientID)
	mustNoErr(t, "read recipient properties", err)
	return asMap(pv)
}

func attachmentProps(t *testing.T, s *Store, attachmentID int64) map[mapi.PropTag]any {
	t.Helper()
	pv, err := s.GetAttachmentProperties(attachmentID)
	mustNoErr(t, "read attachment properties", err)
	return asMap(pv)
}

// wantMessageRow checks the denormalized message row carries the hot columns.
func wantMessageRow(t *testing.T, s *Store, eid, bodyLen int64) {
	t.Helper()
	var (
		parentFID, msgSize int64
		mid                string
		readSt             int
	)
	mustScan(t, s.objdb.QueryRow(
		`SELECT parent_fid, message_size, mid_string, read_state FROM messages WHERE message_id=?`, eid),
		&parentFID, &msgSize, &mid, &readSt)
	wantEq(t, "parent_fid", parentFID, int64(mapi.PrivateFIDInbox))
	wantEq(t, "mid_string", mid, midString(uint64(eid)))
	if msgSize <= bodyLen {
		t.Errorf("message_size = %d, want > body length %d", msgSize, bodyLen)
	}
	wantEq(t, "read_state (delivered unread)", readSt, 0)
}

// wantRecipientBags checks both recipients persisted with their type and
// address.
func wantRecipientBags(t *testing.T, s *Store, eid int64) {
	t.Helper()
	rids := childIDs(t, s, `SELECT recipient_id FROM recipients WHERE message_id=? ORDER BY recipient_id`, eid)
	if len(rids) != 2 {
		t.Fatalf("recipient count = %d, want 2", len(rids))
	}
	r0 := recipientProps(t, s, rids[0])
	wantEq(t, "recipient 0 type", r0[mapi.PrRecipientType], any(int32(mapi.RecipTo)))
	wantEq(t, "recipient 0 address", r0[mapi.PrSmtpAddress], any("bir@example.test"))
	r1 := recipientProps(t, s, rids[1])
	wantEq(t, "recipient 1 type", r1[mapi.PrRecipientType], any(int32(mapi.RecipCc)))
	wantEq(t, "recipient 1 address", r1[mapi.PrSmtpAddress], any("iki@example.test"))
}

// wantAttachmentPayload checks the single attachment and its payload reloaded
// from the content file.
func wantAttachmentPayload(t *testing.T, s *Store, eid int64, attachData []byte) {
	t.Helper()
	aids := childIDs(t, s, `SELECT attachment_id FROM attachments WHERE message_id=? ORDER BY attachment_id`, eid)
	if len(aids) != 1 {
		t.Fatalf("attachment count = %d, want 1", len(aids))
	}
	ap := attachmentProps(t, s, aids[0])
	wantEq(t, "attachment filename", ap[mapi.PrAttachLongFilename], any("rapor.pdf"))
	data, _ := ap[mapi.PrAttachDataBin].([]byte)
	if !bytes.Equal(data, attachData) {
		t.Error("attachment payload did not round-trip through the content offload")
	}
}

package objectstore

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestMessageListAndFlags delivers two messages and verifies the index-backed
// read facade: listing ordered by UID with decoded flags and internal dates,
// single lookup by UID, flag read/write, and that a flag update mirrors the
// read state into the object store. Missing lookups report ErrNotFound.
func TestMessageListAndFlags(t *testing.T) {
	s := openSeededStore(t)

	rawMsg := func(subject string) []byte {
		return []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: " + subject +
			"\r\nDate: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\ngövde.\r\n")
	}
	d1 := time.Unix(1700000000, 0)
	d2 := time.Unix(1700000100, 0)

	i1, err := s.AppendMessage(mapi.PrivateFIDInbox, rawMsg("bir"), d1, 0)
	if err != nil {
		t.Fatal(err)
	}
	i2, err := s.AppendMessage(mapi.PrivateFIDInbox, rawMsg("iki"), d2, FlagSeen)
	if err != nil {
		t.Fatal(err)
	}

	// Listing is ordered by UID and decodes flags + internal date.
	list, err := s.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if list[0].UID != 1 || list[1].UID != 2 {
		t.Errorf("UIDs = %d,%d, want 1,2", list[0].UID, list[1].UID)
	}
	if list[0].Flags != 0 {
		t.Errorf("msg1 flags = %d, want 0", list[0].Flags)
	}
	if list[1].Flags != FlagSeen {
		t.Errorf("msg2 flags = %d, want FlagSeen", list[1].Flags)
	}
	if !list[0].InternalDate.Equal(d1.UTC()) {
		t.Errorf("msg1 internal date = %v, want %v", list[0].InternalDate, d1.UTC())
	}
	if list[0].Size != i1.Size || list[1].Size != i2.Size {
		t.Errorf("sizes = %d,%d, want %d,%d", list[0].Size, list[1].Size, i1.Size, i2.Size)
	}
	// The index surfaces the envelope projections (subject and sender) so a
	// listing needs no per-message wire-form read.
	if list[0].Subject != "bir" || list[1].Subject != "iki" {
		t.Errorf("subjects = %q,%q, want bir,iki", list[0].Subject, list[1].Subject)
	}
	if !strings.Contains(list[0].Sender, "a@example.test") {
		t.Errorf("sender = %q, want it to carry a@example.test", list[0].Sender)
	}
	// AppendMessage returns the same projections it indexed.
	if i1.Subject != "bir" || !strings.Contains(i1.Sender, "a@example.test") {
		t.Errorf("AppendMessage projections = subject %q, sender %q", i1.Subject, i1.Sender)
	}

	// Single lookup by UID.
	m, err := s.MessageByUID(mapi.PrivateFIDInbox, 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != i1.ID {
		t.Errorf("MessageByUID id = %d, want %d", m.ID, i1.ID)
	}
	if _, err := s.MessageByUID(mapi.PrivateFIDInbox, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("MessageByUID(missing) err = %v, want ErrNotFound", err)
	}

	// Set flags, read them back, and confirm the object read_state mirror.
	if err := s.SetMessageFlags(mapi.PrivateFIDInbox, 1, FlagSeen|FlagFlagged); err != nil {
		t.Fatal(err)
	}
	f, err := s.MessageFlags(mapi.PrivateFIDInbox, 1)
	if err != nil {
		t.Fatal(err)
	}
	if f != FlagSeen|FlagFlagged {
		t.Errorf("flags = %d, want FlagSeen|FlagFlagged", f)
	}
	var readState int
	if err := s.objdb.QueryRow(`SELECT read_state FROM messages WHERE message_id=?`, i1.ID).Scan(&readState); err != nil {
		t.Fatal(err)
	}
	if readState != 1 {
		t.Errorf("object read_state = %d, want 1 (mirrored from \\Seen)", readState)
	}

	if err := s.SetMessageFlags(mapi.PrivateFIDInbox, 999, FlagSeen); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetMessageFlags(missing) err = %v, want ErrNotFound", err)
	}
}

// TestSetReadStateFeedsContentSync drives the real read-state write path on a
// non-mail message and confirms the change reaches the ICS read-state download.
// A contact lives only in the object store (the IMAP index holds mail), so this
// also pins that SetMessageReadState resolves the message from the object store,
// not the index — an index-gated path would return ErrNotFound and silently drop
// every calendar/contact read a MAPI client makes.
func TestSetReadStateFeedsContentSync(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid, err := s.CreateMessage(fld, contactMsg("readme"))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetMessageReadState(mid, true); err != nil {
		t.Fatalf("SetMessageReadState on a non-mail message: %v", err)
	}

	var readState int
	var readCN sql.NullInt64
	if err := s.objdb.QueryRow(`SELECT read_state, read_cn FROM messages WHERE message_id=?`, mid).Scan(&readState, &readCN); err != nil {
		t.Fatal(err)
	}
	if readState != 1 {
		t.Errorf("read_state = %d, want 1", readState)
	}
	if !readCN.Valid {
		t.Fatal("read_cn was not allocated on the read-state flip")
	}

	// The body is acknowledged (in Seen) but the read change is not (Read is empty
	// with SYNC_READ_STATE on), so the download must report the message as read.
	res, err := s.GetContentSync(ContentSyncRequest{
		FolderID: fld,
		Given:    looseSet(uint64(mid)),
		Seen:     looseSet(msgCN(t, s, mid)),
		Read:     looseSet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	eqSet(t, "ReadMIDs", res.ReadMIDs, uint64(mid))
	if res.LastReadCN != uint64(readCN.Int64) {
		t.Errorf("LastReadCN = %d, want %d (the read_cn the flip allocated)", res.LastReadCN, readCN.Int64)
	}
}

// TestSetReadStateNoOp pins the "gerçek değişimde" rule: re-setting a message to a
// read state it already holds allocates no new read_cn and does not advance the
// mailbox change-number counter.
func TestSetReadStateNoOp(t *testing.T) {
	s := openSeededStore(t)
	mid, err := s.CreateMessage(int64(mapi.PrivateFIDContacts), contactMsg("noop"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageReadState(mid, true); err != nil {
		t.Fatal(err)
	}
	var firstCN int64
	if err := s.objdb.QueryRow(`SELECT read_cn FROM messages WHERE message_id=?`, mid).Scan(&firstCN); err != nil {
		t.Fatal(err)
	}
	counterBefore := configVal(t, s, cfgLastChangeNumber)

	if err := s.SetMessageReadState(mid, true); err != nil {
		t.Fatal(err)
	}
	var secondCN int64
	if err := s.objdb.QueryRow(`SELECT read_cn FROM messages WHERE message_id=?`, mid).Scan(&secondCN); err != nil {
		t.Fatal(err)
	}
	if secondCN != firstCN {
		t.Errorf("re-setting the same read state reallocated read_cn: %d -> %d", firstCN, secondCN)
	}
	if got := configVal(t, s, cfgLastChangeNumber); got != counterBefore {
		t.Errorf("no-op read-state set bumped the change-number counter: %d -> %d", counterBefore, got)
	}
}

// TestSetMessageFlagsWritesReadCN confirms the IMAP flag-store path also records a
// read_cn when \Seen first appears, so a message read in IMAP propagates to a
// MAPI/ICS client.
func TestSetMessageFlagsWritesReadCN(t *testing.T) {
	s := openSeededStore(t)
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: imapread\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\ngövde.\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetMessageFlags(mapi.PrivateFIDInbox, 1, FlagSeen); err != nil {
		t.Fatal(err)
	}
	var readState int
	var readCN sql.NullInt64
	if err := s.objdb.QueryRow(`SELECT read_state, read_cn FROM messages WHERE message_id=?`, info.ID).Scan(&readState, &readCN); err != nil {
		t.Fatal(err)
	}
	if readState != 1 || !readCN.Valid {
		t.Errorf("after \\Seen: read_state=%d read_cn.valid=%v, want 1/true", readState, readCN.Valid)
	}
}

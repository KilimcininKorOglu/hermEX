package objectstore

import (
	"errors"
	"os"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestDeleteMessage delivers a message, deletes it, and verifies the object
// (with its cascaded children), the index row and mapping, and the cached eml
// are all gone. A repeat delete reports ErrNotFound.
func TestDeleteMessage(t *testing.T) {
	s := openSeededStore(t)

	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: sil\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\ngövde.\r\n")
	info := mustAppendMessage(t, s, mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	mid := midString(uint64(info.ID))

	// Precondition: object, index row, and eml all exist.
	wantRows(t, s, "object rows before delete", 1, `SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID)
	if _, err := os.Stat(s.emlPath(mid)); err != nil {
		t.Fatalf("eml missing before delete: %v", err)
	}

	mustNoErr(t, "delete message", s.DeleteMessage(mapi.PrivateFIDInbox, info.UID))

	// The object and its cascaded children are gone.
	wantRows(t, s, "object rows after delete", 0, `SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID)
	wantRows(t, s, "message properties after delete (cascade)", 0, `SELECT COUNT(*) FROM message_properties WHERE message_id=?`, info.ID)
	wantRows(t, s, "recipients after delete (cascade)", 0, `SELECT COUNT(*) FROM recipients WHERE message_id=?`, info.ID)

	// The index row, mapping, and eml cache are gone.
	wantIdxRows(t, s, "index rows after delete", 0, `SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID)
	wantIdxRows(t, s, "mapping rows after delete", 0, `SELECT COUNT(*) FROM mapping WHERE message_id=?`, info.ID)
	if _, err := os.Stat(s.emlPath(mid)); !os.IsNotExist(err) {
		t.Errorf("eml cache survived delete: stat err = %v", err)
	}

	wantEq(t, "folder message count after delete", len(mustListMessages(t, s, mapi.PrivateFIDInbox)), 0)

	// A repeat delete reports ErrNotFound.
	if err := s.DeleteMessage(mapi.PrivateFIDInbox, info.UID); !errors.Is(err, ErrNotFound) {
		t.Errorf("repeat delete err = %v, want ErrNotFound", err)
	}
}

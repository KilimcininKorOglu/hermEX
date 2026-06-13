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
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	mid := midString(uint64(info.ID))

	// Precondition: object, index row, and eml all exist.
	if countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID) != 1 {
		t.Fatal("object message missing before delete")
	}
	if _, err := os.Stat(s.emlPath(mid)); err != nil {
		t.Fatalf("eml missing before delete: %v", err)
	}

	if err := s.DeleteMessage(mapi.PrivateFIDInbox, info.UID); err != nil {
		t.Fatal(err)
	}

	// The object and its cascaded children are gone.
	if countRows(t, s, `SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID) != 0 {
		t.Error("object message survived delete")
	}
	if countRows(t, s, `SELECT COUNT(*) FROM message_properties WHERE message_id=?`, info.ID) != 0 {
		t.Error("message properties survived delete (cascade failed)")
	}
	if countRows(t, s, `SELECT COUNT(*) FROM recipients WHERE message_id=?`, info.ID) != 0 {
		t.Error("recipients survived delete (cascade failed)")
	}

	// The index row, mapping, and eml cache are gone.
	var idxCount int
	if err := s.idxdb.QueryRow(`SELECT COUNT(*) FROM messages WHERE message_id=?`, info.ID).Scan(&idxCount); err != nil {
		t.Fatal(err)
	}
	if idxCount != 0 {
		t.Error("index row survived delete")
	}
	var mapCount int
	if err := s.idxdb.QueryRow(`SELECT COUNT(*) FROM mapping WHERE message_id=?`, info.ID).Scan(&mapCount); err != nil {
		t.Fatal(err)
	}
	if mapCount != 0 {
		t.Error("mapping row survived delete")
	}
	if _, err := os.Stat(s.emlPath(mid)); !os.IsNotExist(err) {
		t.Errorf("eml cache survived delete: stat err = %v", err)
	}

	list, err := s.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("folder lists %d messages after delete, want 0", len(list))
	}

	// A repeat delete reports ErrNotFound.
	if err := s.DeleteMessage(mapi.PrivateFIDInbox, info.UID); !errors.Is(err, ErrNotFound) {
		t.Errorf("repeat delete err = %v, want ErrNotFound", err)
	}
}

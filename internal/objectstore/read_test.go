package objectstore

import (
	"errors"
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

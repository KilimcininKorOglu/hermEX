package objectstore

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestReindexMessageServesTheEditUnderANewUID edits a read, flagged message and
// reindexes it: the message keeps its id, flags and arrival time under a larger
// UID, the old UID is gone and reported vanished, and the listing, the served
// bytes and the recorded size all follow the edit.
func TestReindexMessageServesTheEditUnderANewUID(t *testing.T) {
	s := openSeededStore(t)
	info := appendForEdit(t, s, "before")
	if err := s.SetMessageFlags(mapi.PrivateFIDInbox, info.UID, FlagSeen|FlagFlagged); err != nil {
		t.Fatal(err)
	}
	if err := s.ModifyMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrSubject, Value: "after"},
		{Tag: mapi.PrNormalizedSubject, Value: "after"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ReindexMessage(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != info.ID || got.UID <= info.UID {
		t.Fatalf("reindexed as id %d uid %d, want id %d under a uid above %d", got.ID, got.UID, info.ID, info.UID)
	}
	if got.Subject != "after" || got.Flags != FlagSeen|FlagFlagged || !got.InternalDate.Equal(info.InternalDate) {
		t.Errorf("reindexed row = %+v, want subject after, the seen and flagged flags and the old arrival time", got)
	}
	wantUIDExpunged(t, s, info.UID)
	wantServedSubject(t, s, got.UID, "after")
}

// wantUIDExpunged holds a UID absent from the inbox and recorded as vanished.
func wantUIDExpunged(t *testing.T, s *Store, uid uint32) {
	t.Helper()
	if _, err := s.MessageByUID(mapi.PrivateFIDInbox, uid); !errors.Is(err, ErrNotFound) {
		t.Errorf("old uid still answers: %v", err)
	}
	vanished, err := s.VanishedSince(mapi.PrivateFIDInbox, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(vanished, uid) {
		t.Errorf("vanished = %v, want the old uid %d", vanished, uid)
	}
}

// wantServedSubject holds the bytes a UID serves to the given subject, with the
// recorded size matching them.
func wantServedSubject(t *testing.T, s *Store, uid uint32, subject string) {
	t.Helper()
	raw, err := s.GetMessageRaw(mapi.PrivateFIDInbox, uid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Subject: "+subject) {
		t.Errorf("uid %d does not serve subject %q:\n%s", uid, subject, raw)
	}
	if size := indexSize(t, s, uid); size != int64(len(raw)) {
		t.Errorf("index reports %d bytes, the message serves %d", size, len(raw))
	}
}

// TestReindexMessageRefusesAnUnindexedObject reports ErrNotFound for a calendar
// item, which never enters the IMAP index.
func TestReindexMessageRefusesAnUnindexedObject(t *testing.T) {
	s := openSeededStore(t)
	id, err := s.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{
		Props: mapi.PropertyValues{{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReindexMessage(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

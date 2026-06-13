package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestFollowupFlagRoundTrip checks that a flagged follow-up (status + color +
// request text + due date) round-trips, and that a flagged status sets the IMAP
// \Flagged bit so IMAP clients still see the message as flagged.
func TestFollowupFlagRoundTrip(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	info, err := s.AppendMessage(inbox, []byte("From: a@b.test\r\nSubject: x\r\n\r\nbody"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}

	due := time.Unix(1800000000, 0).UTC()
	if err := s.SetFollowupFlag(info.ID, FollowupFlag{Status: FlagStatusFlagged, Color: FlagColorRed, Request: "Follow up", DueBy: due}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetFollowupFlag(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != FlagStatusFlagged || got.Color != FlagColorRed || got.Request != "Follow up" {
		t.Errorf("flag = %+v, want flagged/red/\"Follow up\"", got)
	}
	if !got.DueBy.Equal(due) {
		t.Errorf("DueBy = %v, want %v", got.DueBy, due)
	}
	if !messageFlagged(t, s, inbox, info.ID) {
		t.Error("flagged status did not set the IMAP \\Flagged bit")
	}
}

// TestFollowupFlagCompleteAndClear checks that completing a flag records a
// complete time and clears \Flagged, and that clearing returns the message to no
// flag with \Flagged still clear.
func TestFollowupFlagCompleteAndClear(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	info, err := s.AppendMessage(inbox, []byte("From: a@b.test\r\nSubject: y\r\n\r\nbody"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetFollowupFlag(info.ID, FollowupFlag{Status: FlagStatusFlagged, Color: FlagColorBlue}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFollowupFlag(info.ID, FollowupFlag{Status: FlagStatusComplete}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFollowupFlag(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != FlagStatusComplete {
		t.Errorf("status = %d, want complete", got.Status)
	}
	if got.Complete.IsZero() {
		t.Error("complete time not recorded")
	}
	if messageFlagged(t, s, inbox, info.ID) {
		t.Error("completing a flag must clear the IMAP \\Flagged bit")
	}

	if err := s.ClearFollowupFlag(info.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetFollowupFlag(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != FlagStatusNone {
		t.Errorf("status = %d, want none after clear", got.Status)
	}
	if messageFlagged(t, s, inbox, info.ID) {
		t.Error("a cleared flag must not be \\Flagged")
	}
}

// TestCategoriesRoundTrip checks the PidNameKeywords category list round-trips,
// and a message with no categories reads back as none.
func TestCategoriesRoundTrip(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	info, err := s.AppendMessage(inbox, []byte("From: a@b.test\r\nSubject: z\r\n\r\nbody"), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}

	if cats, err := s.GetCategories(info.ID); err != nil || cats != nil {
		t.Fatalf("fresh message categories = %v, %v; want nil, nil", cats, err)
	}
	want := []string{"Work", "Personal"}
	if err := s.SetCategories(info.ID, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCategories(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "Work" || got[1] != "Personal" {
		t.Errorf("categories = %v, want %v", got, want)
	}
}

// messageFlagged reports the IMAP \Flagged bit for a message id, read back
// through the index.
func messageFlagged(t *testing.T, s *Store, folderID, messageID int64) bool {
	t.Helper()
	msgs, err := s.ListMessages(folderID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.ID == messageID {
			return m.Flags&FlagFlagged != 0
		}
	}
	t.Fatalf("message %d not found in folder %d", messageID, folderID)
	return false
}

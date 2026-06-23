package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestFolderSize checks the per-folder size is zero when empty and grows with a
// delivered message.
func TestFolderSize(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)

	if sz, err := s.FolderSize(inbox); err != nil || sz != 0 {
		t.Fatalf("empty folder size = %d (err %v), want 0", sz, err)
	}

	raw := "From: a@b\r\nTo: c@d\r\nSubject: x\r\n\r\nsome body content here\r\n"
	if _, err := s.AppendMessage(inbox, []byte(raw), time.Unix(1, 0), 0); err != nil {
		t.Fatal(err)
	}
	sz, err := s.FolderSize(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if sz <= 0 {
		t.Errorf("folder size after one message = %d, want > 0", sz)
	}
}

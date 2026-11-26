package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestFolderSize checks the per-folder size is zero when empty and is the sum of
// the bytes each of its messages is served as (the RFC822.SIZE a client adds up),
// with a message in another folder left out.
func TestFolderSize(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)

	if sz, err := s.FolderSize(inbox); err != nil || sz != 0 {
		t.Fatalf("empty folder size = %d (err %v), want 0", sz, err)
	}

	raw := "From: a@b\r\nTo: c@d\r\nSubject: x\r\n\r\nsome body content here\r\n"
	first := mustAppendMessage(t, s, inbox, []byte(raw), time.Unix(1, 0), 0)
	second := mustAppendMessage(t, s, inbox, []byte(raw+"and a longer second body\r\n"), time.Unix(2, 0), 0)
	mustAppendMessage(t, s, int64(mapi.PrivateFIDSentItems), []byte(raw), time.Unix(3, 0), 0)

	sz, err := s.FolderSize(inbox)
	mustNoErr(t, "folder size", err)
	served := int64(len(mustGetMessageRaw(t, s, inbox, first.UID)) + len(mustGetMessageRaw(t, s, inbox, second.UID)))
	wantEq(t, "inbox size (the served bytes of its two messages)", sz, served)
}

package objectstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// takeContentFiles reads every stored content file of a mailbox and removes it,
// returning what it read so the caller can put it back. Removing the files makes
// a message's stored content unreadable, which is what an index fill hits when
// the content is on storage that is not mounted yet.
func takeContentFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	saved := map[string][]byte{}
	err := filepath.Walk(filepath.Join(dir, "cid"), func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) // #nosec G304 -- a path this test wrote itself
		if err != nil {
			return err
		}
		saved[p] = b
		return os.Remove(p)
	})
	mustNoErr(t, "take the content files", err)
	if len(saved) == 0 {
		t.Fatal("the mailbox held no content files, so nothing was made unreadable")
	}
	return saved
}

// restoreContentFiles writes back what takeContentFiles removed.
func restoreContentFiles(t *testing.T, saved map[string][]byte) {
	t.Helper()
	for p, b := range saved {
		mustNoErr(t, "restore "+p, os.WriteFile(p, b, 0o600))
	}
}

// TestOpenRetriesAnIndexFillThatLeftMessagesOut is the defect this change fixes.
// A fill whose messages could not be read produced an index that was
// structurally valid and empty. The next open found all three tables and a
// current schema version, read that as a healthy index, and served the mailbox
// empty for good, even after the content was readable again.
func TestOpenRetriesAnIndexFillThatLeftMessagesOut(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	mustNoErr(t, "open", err)
	mustAppendMessage(t, s, int64(mapi.PrivateFIDInbox), checkRaw("one"), time.Unix(1700000000, 0), 0)
	mustNoErr(t, "close", s.Close())

	saved := takeContentFiles(t, dir)
	removeIndexFiles(t, dir)

	partial, err := Open(dir)
	mustNoErr(t, "open while the content is unreadable", err)
	msgs, err := partial.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list", err)
	if len(msgs) != 0 {
		t.Fatalf("the fill indexed %d messages while the content was unreadable", len(msgs))
	}
	mustNoErr(t, "close", partial.Close())
	if !partial.indexFillPending() {
		t.Fatal("a fill that indexed nothing was recorded as finished")
	}

	restoreContentFiles(t, saved)

	s2 := wantInboxHoldsOne(t, dir)
	defer s2.Close()
	if s2.indexFillPending() {
		t.Error("a fill that indexed every message is still recorded as unfinished")
	}
}

// TestOpenKeepsServingWhenOneMessageIsUnreadable locks the other side. One
// message whose content cannot be read must not keep every other message out of
// the index, because that turns a single damaged message into an empty mailbox.
func TestOpenKeepsServingWhenOneMessageIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	mustNoErr(t, "open", err)
	inbox := int64(mapi.PrivateFIDInbox)
	mustAppendMessage(t, s, inbox, checkRaw("first"), time.Unix(1700000000, 0), 0)
	mustNoErr(t, "close", s.Close())

	// The first message loses its content, the second keeps it.
	takeContentFiles(t, dir)

	second, err := Open(dir)
	mustNoErr(t, "reopen", err)
	mustAppendMessage(t, second, inbox, checkRaw("second"), time.Unix(1700000001, 0), 0)
	mustNoErr(t, "close", second.Close())

	removeIndexFiles(t, dir)

	s3, err := Open(dir)
	mustNoErr(t, "open with one unreadable message", err)
	defer s3.Close()
	msgs, err := s3.ListMessages(inbox)
	mustNoErr(t, "list", err)
	if len(msgs) != 1 {
		t.Fatalf("inbox holds %d messages, want the one whose content is readable", len(msgs))
	}
}

package objectstore

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// countFiles reports how many regular files a tree holds, so a case can prove it
// is actually exercising the content copy rather than an empty directory.
func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			n++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return n
}

// TestBackupProducesAnOpenableMailbox is the property that matters: the copy is a
// mailbox, not an archive format. Restoring is copying it back, so it has to open
// and serve the same messages on its own.
func TestBackupProducesAnOpenableMailbox(t *testing.T) {
	s := openSeededStore(t)
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: kept\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nthe body\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}

	// The source store stays open and writable throughout: a backup that only
	// works on an idle mailbox is no use on a running server.
	dest := filepath.Join(t.TempDir(), "copy")
	if err := s.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	copyStore, err := Open(dest)
	if err != nil {
		t.Fatalf("the backup does not open as a mailbox: %v", err)
	}
	defer copyStore.Close()
	msgs, err := copyStore.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("backup holds %d message(s), want 1", len(msgs))
	}
	got, err := copyStore.GetMessageRaw(mapi.PrivateFIDInbox, msgs[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("Subject: kept")) {
		t.Errorf("the backed-up message lost its subject:\n%s", got)
	}
	if len(got) != len(want) {
		t.Errorf("backed-up message is %d bytes, source served %d", len(got), len(want))
	}
}

// TestBackupCarriesContentFiles proves the offloaded content tree comes across.
// An attachment large enough to be written to cid/ is unreadable in the copy if
// only the databases were snapshotted.
func TestBackupCarriesContentFiles(t *testing.T) {
	s := openSeededStore(t)
	body := bytes.Repeat([]byte("payload bytes "), 4096) // large enough to be offloaded
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: with-attachment\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" + string(body) + "\r\n")
	if _, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}

	if countFiles(t, filepath.Join(s.dir, "cid")) == 0 {
		t.Fatal("nothing was offloaded to cid/, so this case would not exercise the content copy")
	}

	dest := filepath.Join(t.TempDir(), "copy")
	if err := s.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if countFiles(t, filepath.Join(dest, "cid")) == 0 {
		t.Error("the backup carried no content files")
	}
	copyStore, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer copyStore.Close()

	msgs, err := copyStore.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("backup holds %d message(s), want 1", len(msgs))
	}
	got, err := copyStore.GetMessageRaw(mapi.PrivateFIDInbox, msgs[0].UID)
	if err != nil {
		t.Fatalf("the backed-up message cannot be served, its content is missing: %v", err)
	}
	if !bytes.Contains(got, []byte("payload bytes payload bytes")) {
		t.Error("the backed-up message lost its body content")
	}
}

// TestBackupIsConsistentAcrossTheSnapshotWindow proves a message delivered in the
// window between the two database snapshots never yields a copy whose IMAP index
// lists a message the objects snapshot lacks. A live append commits the object first
// and the index row last, and Backup snapshots the index first, so the mid-window
// message is either absent from both snapshots or leaves only a harmless orphan
// object, never a listed-but-unfetchable message. Reversing the snapshot order would
// strand an index row with no object, and every listed message would then have to be
// fetchable, so this asserts exactly that.
func TestBackupIsConsistentAcrossTheSnapshotWindow(t *testing.T) {
	s := openSeededStore(t)
	first := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: first\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbefore the backup\r\n")
	if _, err := s.AppendMessage(mapi.PrivateFIDInbox, first, time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}

	// A message delivered exactly between the index snapshot and the objects
	// snapshot: it commits its object, then its index row, in that order.
	s.backupHook = func() {
		window := []byte("From: c@example.test\r\nTo: b@example.test\r\nSubject: window\r\n" +
			"Date: Wed, 15 Nov 2023 10:14:20 +0000\r\n\r\ndelivered mid-backup\r\n")
		if _, err := s.AppendMessage(mapi.PrivateFIDInbox, window, time.Unix(1700000060, 0), 0); err != nil {
			t.Errorf("mid-window append: %v", err)
		}
	}

	dest := filepath.Join(t.TempDir(), "copy")
	if err := s.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	copyStore, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer copyStore.Close()
	msgs, err := copyStore.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	// Every index row the copy lists must resolve to a fetchable body. A dangling
	// index row (the reverse snapshot order) would fail here.
	for _, m := range msgs {
		got, err := copyStore.GetMessageRaw(mapi.PrivateFIDInbox, m.UID)
		if err != nil {
			t.Fatalf("listed message uid %d has no body in the copy (torn snapshot): %v", m.UID, err)
		}
		if len(got) == 0 {
			t.Fatalf("listed message uid %d served empty from the copy", m.UID)
		}
	}
}

// TestBackupOmitsTheEMLCache holds the deliberate exclusion: the cache is a second
// copy of every message that the store rebuilds on demand, so carrying it would
// double the backup for nothing. The message must still serve from the copy.
func TestBackupOmitsTheEMLCache(t *testing.T) {
	s := openSeededStore(t)
	appendForEdit(t, s, "cached") // reads once, so the cache is warm

	dest := filepath.Join(t.TempDir(), "copy")
	if err := s.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(dest, "eml")); err == nil && len(entries) > 0 {
		t.Errorf("the backup carried %d cached wire copies", len(entries))
	}

	copyStore, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer copyStore.Close()
	msgs, err := copyStore.ListMessages(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("backup holds %d message(s), want 1", len(msgs))
	}
	got, err := copyStore.GetMessageRaw(mapi.PrivateFIDInbox, msgs[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("Subject: cached")) {
		t.Errorf("the message did not re-synthesize from the backup:\n%s", got)
	}
}

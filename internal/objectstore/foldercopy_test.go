package objectstore

import (
	"errors"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// miniEML is a minimal RFC822 message for folder-copy tests.
func miniEML(subject string) []byte {
	return []byte("From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: " + subject +
		"\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nbody\r\n")
}

// TestCopyFolder proves the recursive folder copy: a folder with a message and a
// subfolder (itself holding a message) is copied with its contents; a
// non-recursive copy omits the subfolder; and copying a folder into its own
// subtree is refused with ErrFolderCycle.
func TestCopyFolder(t *testing.T) {
	s := openSeededStore(t)
	ipm := int64(mapi.PrivateFIDIPMSubtree)

	src, err := s.CreateFolder(&ipm, "Source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(src, miniEML("top"), time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateFolder(&src, "Sub")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendMessage(sub, miniEML("nested"), time.Now(), 0); err != nil {
		t.Fatal(err)
	}

	// Recursive copy carries the message and the subfolder (with its message).
	newID, err := s.CopyFolder(src, ipm, "Copy", true)
	if err != nil {
		t.Fatalf("CopyFolder recursive: %v", err)
	}
	if msgs, _ := s.ListMessages(newID); len(msgs) != 1 {
		t.Errorf("copied folder messages = %d, want 1", len(msgs))
	}
	children, err := s.childFolderIDs(newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("copied folder subfolders = %d, want 1", len(children))
	}
	if msgs, _ := s.ListMessages(children[0]); len(msgs) != 1 {
		t.Errorf("copied subfolder messages = %d, want 1", len(msgs))
	}

	// Non-recursive copy omits the subfolder but keeps the top message.
	flatID, err := s.CopyFolder(src, ipm, "Flat", false)
	if err != nil {
		t.Fatalf("CopyFolder non-recursive: %v", err)
	}
	if children, _ := s.childFolderIDs(flatID); len(children) != 0 {
		t.Errorf("non-recursive copy subfolders = %d, want 0", len(children))
	}
	if msgs, _ := s.ListMessages(flatID); len(msgs) != 1 {
		t.Errorf("non-recursive copy messages = %d, want 1", len(msgs))
	}

	// The source is untouched by the copies.
	if msgs, _ := s.ListMessages(src); len(msgs) != 1 {
		t.Errorf("source messages after copy = %d, want 1 (copy must not move)", len(msgs))
	}

	// Cycle: copying the source into its own subtree is refused.
	if _, err := s.CopyFolder(src, sub, "Loop", true); !errors.Is(err, ErrFolderCycle) {
		t.Errorf("copy into own subtree err = %v, want ErrFolderCycle", err)
	}
}

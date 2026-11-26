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

	src := mustCreateFolder(t, s, &ipm, "Source")
	mustAppendMessage(t, s, src, miniEML("top"), time.Now(), 0)
	sub := mustCreateFolder(t, s, &src, "Sub")
	mustAppendMessage(t, s, sub, miniEML("nested"), time.Now(), 0)

	// Recursive copy carries the message and the subfolder (with its message).
	newID := mustCopyFolder(t, s, src, ipm, "Copy", true)
	wantEq(t, "copied folder message count", len(mustListMessages(t, s, newID)), 1)
	children, err := s.childFolderIDs(newID)
	mustNoErr(t, "child folder ids", err)
	if len(children) != 1 {
		t.Fatalf("copied folder subfolders = %d, want 1", len(children))
	}
	wantEq(t, "copied subfolder message count", len(mustListMessages(t, s, children[0])), 1)

	// Non-recursive copy omits the subfolder but keeps the top message.
	flatID := mustCopyFolder(t, s, src, ipm, "Flat", false)
	flatChildren, err := s.childFolderIDs(flatID)
	mustNoErr(t, "child folder ids", err)
	wantEq(t, "non-recursive copy subfolder count", len(flatChildren), 0)
	wantEq(t, "non-recursive copy message count", len(mustListMessages(t, s, flatID)), 1)

	// The source is untouched by the copies.
	wantEq(t, "source message count after copy (copy must not move)", len(mustListMessages(t, s, src)), 1)

	// Cycle: copying the source into its own subtree is refused.
	if _, err := s.CopyFolder(src, sub, "Loop", true); !errors.Is(err, ErrFolderCycle) {
		t.Errorf("copy into own subtree err = %v, want ErrFolderCycle", err)
	}
	wantEq(t, "folders created by the refused cycle copy", countFolders(t, s, "Loop"), 0)

	// A copy onto a sibling's name is refused ([MS-OXCFOLD] 3.2.5.2) and adds no
	// second folder under that name.
	if _, err := s.CopyFolder(src, ipm, "Copy", true); !errors.Is(err, ErrFolderExists) {
		t.Errorf("copy onto an existing sibling name err = %v, want ErrFolderExists", err)
	}
	wantEq(t, "folders named Copy after the refused copy", countFolders(t, s, "Copy"), 1)

	// A source id the store does not hold is not found, and creates nothing.
	if _, err := s.CopyFolder(987654, ipm, "Ghost", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("copy of a missing folder err = %v, want ErrNotFound", err)
	}
	wantEq(t, "folders created by the refused missing-source copy", countFolders(t, s, "Ghost"), 0)
}

// mustCopyFolder copies a folder and returns the new folder id.
func mustCopyFolder(t *testing.T, s *Store, src, dstParent int64, name string, recursive bool) int64 {
	t.Helper()
	id, err := s.CopyFolder(src, dstParent, name, recursive)
	mustNoErr(t, "copy folder "+name, err)
	return id
}

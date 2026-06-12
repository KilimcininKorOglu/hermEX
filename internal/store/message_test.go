package store

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestAppendListGetMessage(t *testing.T) {
	s := openTemp(t)
	fid, err := s.CreateFolder(nil, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	idate := time.Unix(1700000000, 0).UTC()
	raw1 := []byte("From: a@example.com\r\nSubject: one\r\n\r\nbody one")
	raw2 := []byte("From: b@example.com\r\nSubject: two\r\n\r\nbody two longer")

	m1, err := s.AppendMessage(fid, raw1, idate, 0)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := s.AppendMessage(fid, raw2, idate, 1)
	if err != nil {
		t.Fatal(err)
	}
	// UIDs are assigned monotonically starting at 1.
	if m1.UID != 1 || m2.UID != 2 {
		t.Fatalf("UIDs = %d, %d, want 1, 2", m1.UID, m2.UID)
	}

	list, err := s.ListMessages(fid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListMessages len = %d, want 2", len(list))
	}
	if list[0].UID != 1 || list[1].UID != 2 {
		t.Errorf("list order = %d, %d, want 1, 2", list[0].UID, list[1].UID)
	}
	if list[1].Size != int64(len(raw2)) {
		t.Errorf("size = %d, want %d", list[1].Size, len(raw2))
	}
	if list[1].Flags != 1 {
		t.Errorf("flags = %d, want 1", list[1].Flags)
	}
	if !list[0].InternalDate.Equal(idate) {
		t.Errorf("internal date = %v, want %v", list[0].InternalDate, idate)
	}

	got, err := s.GetMessageRaw(fid, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw2) {
		t.Errorf("GetMessageRaw = %q, want %q", got, raw2)
	}

	// A missing UID is reported as ErrNotFound, not a generic error.
	if _, err := s.GetMessageRaw(fid, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMessageRaw(missing) err = %v, want ErrNotFound", err)
	}
}

func TestAppendToMissingFolder(t *testing.T) {
	s := openTemp(t)
	if _, err := s.AppendMessage(424242, []byte("x"), time.Unix(0, 0), 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("append to missing folder err = %v, want ErrNotFound", err)
	}
}

func TestListFolders(t *testing.T) {
	s := openTemp(t)
	root, err := s.CreateFolder(nil, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFolder(&root, "Subfolder"); err != nil {
		t.Fatal(err)
	}
	folders, err := s.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 2 {
		t.Fatalf("ListFolders len = %d, want 2", len(folders))
	}
	if folders[0].DisplayName != "Inbox" || folders[0].ParentID != nil {
		t.Errorf("root folder = %#v", folders[0])
	}
	if folders[1].ParentID == nil || *folders[1].ParentID != root {
		t.Errorf("subfolder parent = %v, want %d", folders[1].ParentID, root)
	}
}

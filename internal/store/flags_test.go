package store

import (
	"errors"
	"testing"
	"time"
)

func TestSetMessageFlags(t *testing.T) {
	s := openTemp(t)
	fid, err := s.CreateFolder(nil, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.AppendMessage(fid, []byte("Subject: x\r\n\r\nbody"), time.Unix(0, 0), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Distinct bits so a mask/shift bug surfaces: \Seen|\Deleted = 1|8 = 9.
	want := FlagSeen | FlagDeleted
	if err := s.SetMessageFlags(fid, m.UID, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	list, err := s.ListMessages(fid)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Flags != want {
		t.Fatalf("flags = %d, want %d", list[0].Flags, want)
	}

	// Replacing with a different set must not OR with the old value.
	if err := s.SetMessageFlags(fid, m.UID, FlagAnswered); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if list, _ = s.ListMessages(fid); list[0].Flags != FlagAnswered {
		t.Fatalf("flags after replace = %d, want %d", list[0].Flags, FlagAnswered)
	}
}

func TestSetMessageFlagsMissing(t *testing.T) {
	s := openTemp(t)
	fid, err := s.CreateFolder(nil, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageFlags(fid, 999, FlagSeen); !errors.Is(err, ErrNotFound) {
		t.Errorf("set missing err = %v, want ErrNotFound", err)
	}
}

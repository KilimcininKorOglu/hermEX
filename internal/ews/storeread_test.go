package ews

import (
	"testing"

	"hermex/internal/objectstore"
)

// mustListMessages lists a folder's live messages and fails the test on a read
// error, so a count or emptiness check never passes on a failed read.
func mustListMessages(t *testing.T, st *objectstore.Store, folderID int64) []objectstore.MessageInfo {
	t.Helper()
	msgs, err := st.ListMessages(folderID)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// mustListSoftDeleted lists a folder's recoverable messages and fails the test
// on a read error.
func mustListSoftDeleted(t *testing.T, st *objectstore.Store, folderID int64) []objectstore.SoftDeletedMessage {
	t.Helper()
	dump, err := st.ListSoftDeleted(folderID)
	if err != nil {
		t.Fatal(err)
	}
	return dump
}

// mustListAllSoftDeleted lists every recoverable message of the mailbox and
// fails the test on a read error.
func mustListAllSoftDeleted(t *testing.T, st *objectstore.Store) []objectstore.SoftDeletedItem {
	t.Helper()
	dump, err := st.ListAllSoftDeleted()
	if err != nil {
		t.Fatal(err)
	}
	return dump
}

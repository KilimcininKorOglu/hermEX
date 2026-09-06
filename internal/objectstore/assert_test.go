package objectstore

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"hermex/internal/oxcmail"
)

// The store tests are assertion-dense: one test walks a stored object and
// checks a dozen projected columns. Written out, every check is an `if` in the
// test body, which is what pushes these functions past the complexity budget
// and buries the one line that states what the test is about. The helpers here
// carry the comparison and the failure message so a test body reads as the list
// of facts it asserts.

// wantEq fails when got differs from want, naming the field in the message.
func wantEq[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// wantNotEq fails when got equals the value the test requires it to differ
// from.
func wantNotEq[T comparable](t *testing.T, label string, got, unwanted T) {
	t.Helper()
	if got == unwanted {
		t.Errorf("%s = %v, want anything else", label, got)
	}
}

// mustNoErr fails the test when err is set, naming the operation that produced
// it.
func mustNoErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantContains fails when got does not carry the substring the test requires.
func wantContains(t *testing.T, label, got, substr string) {
	t.Helper()
	if !strings.Contains(got, substr) {
		t.Errorf("%s = %q, want it to contain %q", label, got, substr)
	}
}

// wantRows checks the row count a COUNT(*) query reports against the object
// store.
func wantRows(t *testing.T, s *Store, label string, want int, query string, args ...any) {
	t.Helper()
	wantEq(t, label, countRows(t, s, query, args...), want)
}

// wantErr fails when an operation the test requires to be refused succeeded.
func wantErr(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error, want one", what)
	}
}

// wantIdxRows checks the row count a COUNT(*) query reports against the IMAP
// index.
func wantIdxRows(t *testing.T, s *Store, label string, want int, query string, args ...any) {
	t.Helper()
	var n int
	mustScan(t, s.idxdb.QueryRow(query, args...), &n)
	wantEq(t, label, n, want)
}

// mustScan runs a single-row query to completion, so a test states the columns
// it reads rather than the error path it never takes.
func mustScan(t *testing.T, row *sql.Row, dest ...any) {
	t.Helper()
	if err := row.Scan(dest...); err != nil {
		t.Fatalf("scan: %v", err)
	}
}

// mustCreateFolder creates a folder and returns its id.
func mustCreateFolder(t *testing.T, s *Store, parent *int64, displayName string) int64 {
	t.Helper()
	id, err := s.CreateFolder(parent, displayName)
	if err != nil {
		t.Fatalf("create folder %q: %v", displayName, err)
	}
	return id
}

// mustCreateMessage stores an object and returns its message id.
func mustCreateMessage(t *testing.T, s *Store, folderID int64, msg *oxcmail.Message) int64 {
	t.Helper()
	id, err := s.CreateMessage(folderID, msg)
	if err != nil {
		t.Fatalf("create message: %v", err)
	}
	return id
}

// mustIndexMessage adds an already-stored object to the IMAP index and returns
// the UID it was allocated.
func mustIndexMessage(t *testing.T, s *Store, folderID, messageID int64, msg *oxcmail.Message, wireSize int64, received time.Time, flags int64) int64 {
	t.Helper()
	uid, err := s.indexMessage(folderID, messageID, midString(uint64(messageID)), msg, wireSize, received, flags)
	if err != nil {
		t.Fatalf("index message: %v", err)
	}
	return uid
}

// mustAppendMessage delivers a wire-form message and returns its index row.
func mustAppendMessage(t *testing.T, s *Store, folderID int64, raw []byte, internalDate time.Time, flags int64) MessageInfo {
	t.Helper()
	info, err := s.AppendMessage(folderID, raw, internalDate, flags)
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
	return info
}

// mustListMessages returns a folder's index rows.
func mustListMessages(t *testing.T, s *Store, folderID int64) []MessageInfo {
	t.Helper()
	msgs, err := s.ListMessages(folderID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	return msgs
}

// mustOpenMessage reads a stored object back.
func mustOpenMessage(t *testing.T, s *Store, messageID int64) *oxcmail.Message {
	t.Helper()
	msg, err := s.OpenMessage(messageID)
	if err != nil {
		t.Fatalf("open message: %v", err)
	}
	return msg
}

// mustGetMessageRaw returns the wire form served for one index row.
func mustGetMessageRaw(t *testing.T, s *Store, folderID int64, uid uint32) []byte {
	t.Helper()
	raw, err := s.GetMessageRaw(folderID, uid)
	if err != nil {
		t.Fatalf("get message raw: %v", err)
	}
	return raw
}

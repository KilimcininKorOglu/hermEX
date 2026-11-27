package meeting

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// mustNoErr stops the test when a step failed.
func mustNoErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// mustClose closes a store or database and fails the test when the close fails,
// since a store that cannot close may not have written what the test asserted.
func mustClose(t *testing.T, c interface{ Close() error }, what string) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close %s: %v", what, err)
	}
}

// processReply runs ProcessReply and fails the test on an error, so a refused
// reply is told apart from a failed tracking write: a refusal answers (false, nil).
func processReply(t *testing.T, st *objectstore.Store, sender string, messageID int64) bool {
	t.Helper()
	handled, err := ProcessReply(st, nil, sender, messageID)
	mustNoErr(t, err, "process the reply")
	return handled
}

// calendarItems lists the Calendar folder and fails the test when the read fails.
func calendarItems(t *testing.T, st *objectstore.Store) []objectstore.FolderObject {
	t.Helper()
	cal, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	mustNoErr(t, err, "list the calendar")
	return cal
}

// wantTrue fails the test unless the fact holds.
func wantTrue(t *testing.T, got bool, what string) {
	t.Helper()
	if !got {
		t.Errorf("%s is false, want true", what)
	}
}

// mustCount stops the test unless a list holds the expected number of elements,
// because the following assertions index into it.
func mustCount(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", what, got, want)
	}
}

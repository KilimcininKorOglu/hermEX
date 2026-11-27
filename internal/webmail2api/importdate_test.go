package webmail2api

import (
	"net/http"
	"testing"
	"time"

	"hermex/internal/avtest"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// inboxDates returns the internal date of every message in the mailbox's inbox.
func inboxDates(t *testing.T, mbox string) []time.Time {
	t.Helper()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]time.Time, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.InternalDate)
	}
	return out
}

// TestImportKeepsTheMessageDate is the archive restore. An import is not an
// arrival: filing a years-old message under the moment it was uploaded puts it at
// the top of a date-ordered folder, where new mail belongs, and the user cannot get
// it back to where it should be.
func TestImportKeepsTheMessageDate(t *testing.T) {
	withScanner(t, avtest.Clean)
	srv, token, mbox := importHarness(t)

	const eml = "Subject: from the archive\r\nDate: Tue, 15 Jan 2019 09:30:00 +0000\r\n\r\nbody\r\n"
	if code, body := importEML(t, srv, token, eml); code != http.StatusOK {
		t.Fatalf("import status = %d: %s", code, body)
	}
	dates := inboxDates(t, mbox)
	if len(dates) != 1 {
		t.Fatalf("inbox holds %d messages, want 1", len(dates))
	}
	want := time.Date(2019, 1, 15, 9, 30, 0, 0, time.UTC)
	if !dates[0].Equal(want) {
		t.Errorf("internal date = %s, want the message's own %s", dates[0], want)
	}
}

// TestImportWithoutADateFallsBackToNow keeps a message that carries no usable Date
// filed under the import, which is also what IMAP APPEND does for a client that
// supplies no date.
func TestImportWithoutADateFallsBackToNow(t *testing.T) {
	withScanner(t, avtest.Clean)
	srv, token, mbox := importHarness(t)

	before := time.Now().Add(-time.Minute)
	// No Date at all, and a Date that is not an RFC 5322 date-time.
	for _, eml := range []string{"Subject: undated\r\n\r\nbody\r\n", "Subject: garbled\r\nDate: the day before yesterday\r\n\r\nbody\r\n"} {
		if code, body := importEML(t, srv, token, eml); code != http.StatusOK {
			t.Fatalf("import status = %d: %s", code, body)
		}
	}
	dates := inboxDates(t, mbox)
	if len(dates) != 2 {
		t.Fatalf("inbox holds %d messages, want 2", len(dates))
	}
	for _, d := range dates {
		if d.Before(before) {
			t.Errorf("internal date = %s, want the import time (after %s)", d, before)
		}
	}
}

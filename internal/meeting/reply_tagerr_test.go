package meeting

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"

	_ "modernc.org/sqlite"
)

// TestProcessReplyReportsATagResolutionFailure proves an authorized REPLY whose
// tracking tags cannot be resolved is reported as handled with the error, not as
// an unhandled message: the caller logs only a returned error, so a swallowed one
// leaves the organizer's Tracking tab stale with nothing in the log.
func TestProcessReplyReportsATagResolutionFailure(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustClose(t, st, "the store") })
	msgID := deliverReply(t, st, "meeting-tags", "bob@hermex.test", "ACCEPTED")

	db, err := sql.Open("sqlite", filepath.Join(dir, "objects.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, db, "the raw database handle")
	if _, err := db.Exec(`CREATE TRIGGER refuse_names BEFORE INSERT ON named_properties
		BEGIN SELECT RAISE(ABORT, 'injected name failure'); END`); err != nil {
		t.Fatal(err)
	}

	handled, err := ProcessReply(st, nil, "bob@hermex.test", msgID)
	if !handled || err == nil {
		t.Fatalf("ProcessReply = %v, %v; want handled with the tag error", handled, err)
	}
}

// TestAFailedOpenIsNotAMissingMessage breaks the one read that opens the named
// message, with the message present, and calls every entry point that opens one.
// A missing message is ErrRequestNotFound, which the protocol surfaces answer as a
// client error (404, ErrorItemNotFound); a store failure must reach them as itself,
// or a broken mailbox reads to the client as a message it named wrongly.
func TestAFailedOpenIsNotAMissingMessage(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	mustNoErr(t, err, "Open")
	t.Cleanup(func() { mustClose(t, st, "the store") })
	mustNoErr(t, st.SetMeetingConfig(objectstore.MeetingConfig{AutoAccept: true}), "enable auto-accept")
	id, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: requestClass},
	}})
	mustNoErr(t, err, "CreateMessage")
	_, _, err = Describe(st, id+1000, nil)
	if !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("Describe of a missing message: err = %v, want ErrRequestNotFound", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, "objects.sqlite3"))
	mustNoErr(t, err, "open the raw database")
	defer mustClose(t, db, "the raw database handle")
	if _, err := db.Exec(`ALTER TABLE recipients RENAME TO recipients_gone`); err != nil {
		t.Fatal(err)
	}

	for name, call := range map[string]func() error{
		"Describe":       func() error { _, _, err := Describe(st, id, nil); return err },
		"RemoveCanceled": func() error { return RemoveCanceled(st, id, nil) },
		"Respond": func() error {
			_, err := Respond(st, nil, nil, "alice@hermex.test", id, ResponseAccepted, false)
			return err
		},
		"AutoProcess": func() error { _, err := AutoProcess(st, nil, nil, "room@hermex.test", id); return err },
	} {
		if err := call(); err == nil || errors.Is(err, ErrRequestNotFound) {
			t.Errorf("%s on an unreadable message: err = %v, want the store's error", name, err)
		}
	}
}

// TestDeliveryPassesReportAFailedRead proves a store read that fails while a
// delivered REPLY or CANCEL is read is reported, not taken for "no calendar part":
// the delivery pass logs only a returned error, so reading the failure as an
// ordinary mail loses the tracking or cancellation with nothing in the log.
func TestDeliveryPassesReportAFailedRead(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	mustNoErr(t, err, "Open")
	msgID := deliverReply(t, st, "meeting-read", "bob@hermex.test", "ACCEPTED")
	mustClose(t, st, "the store")

	if _, err := ProcessReply(st, nil, "bob@hermex.test", msgID); err == nil {
		t.Error("ProcessReply on an unreadable store reported no error")
	}
	if _, err := ProcessCancellation(st, "bob@hermex.test", msgID); err == nil {
		t.Error("ProcessCancellation on an unreadable store reported no error")
	}
}

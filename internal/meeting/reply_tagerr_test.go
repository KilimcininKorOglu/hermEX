package meeting

import (
	"database/sql"
	"path/filepath"
	"testing"

	"hermex/internal/objectstore"

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

package objectstore

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestBackfillPreviewsRepairsAnOldRow proves the upgrade path. A store indexed
// before the columns existed lists every message with a blank snippet and no
// paperclip, and a migration cannot compute either: the snippet comes from the
// body and the flag from the attachment rows, which SQL cannot read.
func TestBackfillPreviewsRepairsAnOldRow(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	withFile := "From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: invoice\r\n" +
		"Content-Type: multipart/mixed; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\nplease see attached\r\n" +
		"--b1\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"i.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--b1--\r\n"
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(withFile), time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	// Put the row back into the state an older store left it in.
	if _, err := st.idxdb.Exec(`UPDATE messages SET preview='', has_attach=0`); err != nil {
		t.Fatal(err)
	}

	n, err := st.BackfillPreviews()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("backfilled %d rows, want 1", n)
	}
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(msgs[0].Preview, "please see attached") {
		t.Errorf("preview after backfill = %q", msgs[0].Preview)
	}
	if !msgs[0].HasAttachments {
		t.Error("the attachment flag was not backfilled")
	}

	assertBackfillIsIdempotent(t, st)
}

// assertBackfillIsIdempotent proves a repeated run reports no work, so an
// operator running it twice does not read it as having repaired more rows.
func assertBackfillIsIdempotent(t *testing.T, st *Store) {
	t.Helper()
	again, err := st.BackfillPreviews()
	if err != nil || again != 0 {
		t.Errorf("second run = %d rows, err %v, want 0", again, err)
	}
}

// TestBackfillPreviewsSkipsAMessageWithNothingToRecord keeps a genuinely empty
// message from being counted, and from being revisited on every run.
func TestBackfillPreviewsSkipsAMessageWithNothingToRecord(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := "From: a@hermex.test\r\nTo: b@hermex.test\r\nSubject: empty\r\n\r\n"
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	n, err := st.BackfillPreviews()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("backfilled %d rows, want 0 for a message with no body and no attachment", n)
	}
}

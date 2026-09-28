package objectstore

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestRepairDatelessMessages proves a message stored without a Date header and
// dated by its store time is re-dated by its arrival on the next open, while a
// message that carried its own Date keeps it, and the pass runs once.
func TestRepairDatelessMessages(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	mustNoErr(t, "open", err)
	arrived := time.Date(2024, 6, 12, 13, 46, 40, 0, time.UTC)
	stored := time.Date(2026, 6, 13, 21, 16, 52, 0, time.UTC)
	inbox := int64(mapi.PrivateFIDInbox)
	dateless := mustAppendMessage(t, st, inbox, []byte("From: ops@hermex.test\r\nSubject: Welcome\r\n\r\nready\r\n"), arrived, 0)
	dated := mustAppendMessage(t, st, inbox, []byte("From: ops@hermex.test\r\nDate: Sat, 13 Jun 2026 21:16:52 +0000\r\nSubject: Ahead\r\n\r\nclock ahead\r\n"), arrived, 0)
	// The state an append left before the fix: the store time as the submit time.
	mustNoErr(t, "backdate", st.ModifyMessageProperties(dateless.ID, mapi.PropertyValues{{Tag: mapi.PrClientSubmitTime, Value: mapi.UnixToNTTime(stored)}}))
	_, err = st.objdb.Exec(`DELETE FROM configurations WHERE config_id=?`, cfgDatelessRepaired)
	mustNoErr(t, "clear the marker", err)
	mustNoErr(t, "close", st.Close())

	st, err = Open(dir)
	mustNoErr(t, "reopen", err)
	defer st.Close()
	wantEq(t, "the re-dated submit time", submitTime(t, st, dateless.ID), any(mapi.UnixToNTTime(arrived)))
	wantContains(t, "the served message", string(mustGetMessageRaw(t, st, inbox, dateless.UID)), "Date: Wed, 12 Jun 2024 13:46:40 +0000")
	wantEq(t, "the submit time of a message with its own Date", submitTime(t, st, dated.ID), any(mapi.UnixToNTTime(stored)))
	var done int64
	mustScan(t, st.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgDatelessRepaired), &done)
	wantEq(t, "the repair marker", done, int64(1))
}

// submitTime reads a message's stored submit time.
func submitTime(t *testing.T, st *Store, id int64) any {
	t.Helper()
	pv, err := st.GetMessageProperties(id, mapi.PrClientSubmitTime)
	mustNoErr(t, "read the submit time", err)
	v, _ := pv.Get(mapi.PrClientSubmitTime)
	return v
}

// TestHasDateHeader proves only a Date field name at the start of a header line
// counts.
func TestHasDateHeader(t *testing.T) {
	cases := map[string]bool{
		"From: a\r\nDate: Wed, 12 Jun 2024 13:46:40 +0000\r\n\r\n": true,
		"From: a\r\ndate: x\r\n":                                   true,
		"From: a\r\nSubject: Date: tomorrow\r\n\r\n":               false,
		"From: a\r\n\r\nDate: in the body\r\n":                     false,
		"X-Date: x\r\n":                                            false,
	}
	for in, want := range cases {
		if got := hasDateHeader(in); got != want {
			t.Errorf("hasDateHeader(%q) = %v, want %v", in, got, want)
		}
	}
}

package webmail2api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestMailFollowupSetsRichFlag proves POST /mail/followup ports the old webmail's
// rich follow-up flag: a coloured flag with a due date, mark-complete, and clear,
// rather than the plain \Flagged star webmail2 had before.
func TestMailFollowupSetsRichFlag(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	raw := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: ping\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	mustNoErr(t, "append", err)
	uid := info.UID
	st.Close()

	do := apiHarnessFor(t, dir)
	id := "inbox:" + strconv.FormatUint(uint64(uid), 10)
	post := func(what, body string) {
		t.Helper()
		wantStatus(t, what, do(http.MethodPost, "/api/v1/mail/followup", body), http.StatusOK)
	}

	due := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	post("flag", `{"id":"`+id+`","action":"flag","color":5,"due":"`+due.Format(time.RFC3339)+`"}`)
	f := storedFollowup(t, dir, uid)
	wantEq(t, "status after flag", f.Status, int32(objectstore.FlagStatusFlagged))
	wantEq(t, "colour after flag (blue)", f.Color, int32(objectstore.FlagColorBlue))
	if !f.DueBy.Equal(due) {
		t.Errorf("due after flag = %v, want %v", f.DueBy, due)
	}

	post("complete", `{"id":"`+id+`","action":"complete"}`)
	wantEq(t, "status after complete", storedFollowup(t, dir, uid).Status, int32(objectstore.FlagStatusComplete))

	post("clear", `{"id":"`+id+`","action":"clear"}`)
	wantEq(t, "status after clear", storedFollowup(t, dir, uid).Status, int32(objectstore.FlagStatusNone))
}

// storedFollowup reads the follow-up flag the store holds for an inbox message.
func storedFollowup(t *testing.T, dir string, uid uint32) objectstore.FollowupFlag {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "reopen mailbox", err)
	defer st.Close()
	m, err := st.MessageByUID(int64(mapi.PrivateFIDInbox), uid)
	mustNoErr(t, "message by uid", err)
	f, err := st.GetFollowupFlag(m.ID)
	mustNoErr(t, "get followup flag", err)
	return f
}

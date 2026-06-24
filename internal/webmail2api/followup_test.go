package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestMailFollowupSetsRichFlag proves POST /mail/followup ports the old webmail's
// rich follow-up flag: a coloured flag with a due date, mark-complete, and clear,
// rather than the plain \Flagged star webmail2 had before.
func TestMailFollowupSetsRichFlag(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	raw := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: ping\r\n\r\nhi\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	uid := info.UID
	st.Close()

	secret := []byte("followup-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	id := "inbox:" + strconv.FormatUint(uint64(uid), 10)

	post := func(body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/followup", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	readFlag := func(t *testing.T) objectstore.FollowupFlag {
		t.Helper()
		st, err := objectstore.Open(dir)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer st.Close()
		m, err := st.MessageByUID(int64(mapi.PrivateFIDInbox), uid)
		if err != nil {
			t.Fatalf("by uid: %v", err)
		}
		f, err := st.GetFollowupFlag(m.ID)
		if err != nil {
			t.Fatalf("get flag: %v", err)
		}
		return f
	}

	due := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	if rec := post(`{"id":"` + id + `","action":"flag","color":5,"due":"` + due.Format(time.RFC3339) + `"}`); rec.Code != 200 {
		t.Fatalf("flag status=%d body=%s", rec.Code, rec.Body.String())
	}
	if f := readFlag(t); f.Status != objectstore.FlagStatusFlagged || f.Color != objectstore.FlagColorBlue || !f.DueBy.Equal(due) {
		t.Errorf("flagged = {status:%d color:%d due:%v}, want flagged+blue(5)+%v", f.Status, f.Color, f.DueBy, due)
	}

	if rec := post(`{"id":"` + id + `","action":"complete"}`); rec.Code != 200 {
		t.Fatalf("complete status=%d", rec.Code)
	}
	if f := readFlag(t); f.Status != objectstore.FlagStatusComplete {
		t.Errorf("after complete, status=%d, want complete(1)", f.Status)
	}

	if rec := post(`{"id":"` + id + `","action":"clear"}`); rec.Code != 200 {
		t.Fatalf("clear status=%d", rec.Code)
	}
	if f := readFlag(t); f.Status != objectstore.FlagStatusNone {
		t.Errorf("after clear, status=%d, want none(0)", f.Status)
	}
}

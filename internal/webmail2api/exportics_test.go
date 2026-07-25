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

// TestExportICS proves GET /mail/export-ics streams a message's embedded
// text/calendar part verbatim as an .ics download (preserving the iTIP METHOD),
// derives the filename from SUMMARY, and 404s for a message with no invite.
func TestExportICS(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	inbox := int64(mapi.PrivateFIDInbox)

	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:evt-123@test\r\nSUMMARY:Team Sync\r\n" +
		"DTSTART:20260101T090000Z\r\nDTEND:20260101T093000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	invite := "From: org@b.test\r\nSubject: Invite\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=bb\r\n\r\n" +
		"--bb\r\nContent-Type: text/plain\r\n\r\nPlease join.\r\n\r\n" +
		"--bb\r\nContent-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\n" +
		ics + "\r\n--bb--\r\n"
	plain := "From: x@b.test\r\nSubject: no invite\r\n\r\njust text\r\n"

	iInvite, err := st.AppendMessage(inbox, []byte(invite), time.Now(), 0)
	if err != nil {
		t.Fatalf("append invite: %v", err)
	}
	iPlain, err := st.AppendMessage(inbox, []byte(plain), time.Now(), 0)
	if err != nil {
		t.Fatalf("append plain: %v", err)
	}
	st.Close()

	secret := []byte("export-ics-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	get := func(id string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/export-ics?id="+id, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	uInvite := "inbox:" + strconv.FormatUint(uint64(iInvite.UID), 10)
	rec := get(uInvite)
	if rec.Code != http.StatusOK {
		t.Fatalf("export invite: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Errorf("Content-Type = %q, want text/calendar", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "Team_Sync.ics") {
		t.Errorf("Content-Disposition = %q, want Team_Sync.ics", cd)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "METHOD:REQUEST") || !strings.Contains(body, "UID:evt-123@test") {
		t.Errorf("exported ics missing iTIP METHOD or UID:\n%s", body)
	}

	uPlain := "inbox:" + strconv.FormatUint(uint64(iPlain.UID), 10)
	if rec := get(uPlain); rec.Code != http.StatusNotFound {
		t.Errorf("plain message export = %d, want 404", rec.Code)
	}
}

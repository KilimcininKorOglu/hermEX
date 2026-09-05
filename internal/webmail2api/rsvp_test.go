package webmail2api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// inviteMail is a delivered meeting request: a mail carrying an iTIP REQUEST, the
// shape the store imports into a meeting request with its appointment properties.
func inviteMail(uid string) []byte {
	return []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Quarterly review\r\n" +
		"Date: Mon, 01 Jun 2026 10:00:00 +0000\r\n" +
		"Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\nDTSTART:20260615T090000Z\r\nDTEND:20260615T100000Z\r\n" +
		"SUMMARY:Quarterly review\r\nORGANIZER:mailto:bob@hermex.test\r\n" +
		"ATTENDEE;RSVP=TRUE:mailto:alice@hermex.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
}

// rsvpHarness seeds one delivered invitation and returns a request helper plus the
// mailbox path and the SPA's opaque id for that mail.
func rsvpHarness(t *testing.T) (func(body string) *httptest.ResponseRecorder, string, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), inviteMail("review-1@hermex.test"), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	secret := []byte("rsvp-test-secret")
	accs := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: dir},
		"bob@hermex.test":   {MailboxPath: t.TempDir()},
	}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)
	id := fmt.Sprintf("inbox:%d", info.UID)
	return func(body string) *httptest.ResponseRecorder {
		token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/rsvp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}, dir, id
}

// calendarCount counts the appointments in the mailbox.
func calendarCount(t *testing.T, mbox string) int {
	t.Helper()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil {
		t.Fatal(err)
	}
	return len(objs)
}

// TestRSVPAcceptedTwiceFilesOneAppointment is the defect. The webmail answer used
// to file a fresh appointment each time, with no regard for one already there, so
// answering twice, or answering after the server had auto-processed the
// invitation, left two appointments for one meeting.
func TestRSVPAcceptedTwiceFilesOneAppointment(t *testing.T) {
	do, mbox, id := rsvpHarness(t)

	for i := range 2 {
		rec := do(`{"id":"` + id + `","response":"accept"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("accept %d = %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if n := calendarCount(t, mbox); n != 1 {
		t.Errorf("the calendar holds %d appointments after answering twice, want 1", n)
	}
}

// TestRSVPDeclineIsRecorded proves declining does something. It used to return a
// status and record nothing at all.
func TestRSVPDeclineIsRecorded(t *testing.T) {
	do, mbox, id := rsvpHarness(t)

	if rec := do(`{"id":"` + id + `","response":"accept"}`); rec.Code != http.StatusOK {
		t.Fatalf("accept = %d: %s", rec.Code, rec.Body.String())
	}
	if n := calendarCount(t, mbox); n != 1 {
		t.Fatalf("accept filed %d appointments, want 1", n)
	}
	if rec := do(`{"id":"` + id + `","response":"decline"}`); rec.Code != http.StatusOK {
		t.Fatalf("decline = %d: %s", rec.Code, rec.Body.String())
	}
	// Declining takes the meeting off the calendar, as it does on every other
	// protocol.
	if n := calendarCount(t, mbox); n != 0 {
		t.Errorf("the calendar still holds %d appointments after declining, want 0", n)
	}
}

// TestRSVPRefusesAnUnknownResponse keeps a word the model has no value for from
// being recorded as one.
func TestRSVPRefusesAnUnknownResponse(t *testing.T) {
	do, _, id := rsvpHarness(t)
	rec := do(`{"id":"` + id + `","response":"maybe"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown response = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestRSVPClearsTheRequestWhenConfigured proves the mailbox's opt-in reaches the
// webmail answer, not only the EWS and ActiveSync ones.
func TestRSVPClearsTheRequestWhenConfigured(t *testing.T) {
	do, mbox, id := rsvpHarness(t)
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeetingConfig(objectstore.MeetingConfig{RemoveRequestOnResponse: true}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if rec := do(`{"id":"` + id + `","response":"accept"}`); rec.Code != http.StatusOK {
		t.Fatalf("accept = %d: %s", rec.Code, rec.Body.String())
	}
	st, err = objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 0 {
		t.Errorf("the Inbox still holds %d messages, want the answered request moved out", len(objs))
	}
}

// TestRSVPReportsTheStatus keeps the response body the SPA reads.
func TestRSVPReportsTheStatus(t *testing.T) {
	do, _, id := rsvpHarness(t)
	rec := do(`{"id":"` + id + `","response":"tentative"}`)
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "tentativeed" {
		t.Errorf("status = %q", out.Status)
	}
}

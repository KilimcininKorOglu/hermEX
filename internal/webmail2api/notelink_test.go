package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// noteLinkHarness seeds a mailbox with one mail and returns a request helper.
func noteLinkHarness(t *testing.T) func(method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Contract\r\n" +
		"Message-ID: <contract-1@hermex.test>\r\nDate: Mon, 01 Jun 2026 10:00:00 +0000\r\n\r\nbody\r\n")
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	// A second mail with its own Message-ID, so a note on the first must not show
	// up on it.
	other := []byte("From: bob@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Other\r\n" +
		"Message-ID: <other-1@hermex.test>\r\nDate: Mon, 01 Jun 2026 11:00:00 +0000\r\n\r\nbody\r\n")
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), other, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()

	secret := []byte("note-link-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	return func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
}

// mailNotes reads the notes annotating one mail.
func mailNotes(t *testing.T, do func(string, string, string) *httptest.ResponseRecorder, mailID string) []noteJSON {
	t.Helper()
	rec := do(http.MethodGet, "/api/v1/mail/notes?id="+mailID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("mail notes: status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Notes []noteJSON `json:"notes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Notes
}

// TestNoteLinkedToMail proves a note annotates one mail and only that mail. The
// link is the mail's Message-ID rather than its store id, so it survives the mail
// being moved between folders, and nothing is written to the mail itself.
func TestNoteLinkedToMail(t *testing.T) {
	do := noteLinkHarness(t)

	if rec := do(http.MethodPost, "/api/v1/notes",
		`{"title":"Chase this","body":"call legal","linkedMessageId":"<contract-1@hermex.test>"}`); rec.Code != http.StatusOK {
		t.Fatalf("create note: status %d body %s", rec.Code, rec.Body.String())
	}

	notes := mailNotes(t, do, "inbox:1")
	if len(notes) != 1 {
		t.Fatalf("the annotated mail has %d notes, want 1", len(notes))
	}
	if notes[0].Title != "Chase this" || notes[0].Body != "call legal" {
		t.Errorf("note = %+v", notes[0])
	}
	if notes[0].LinkedMessageID != "<contract-1@hermex.test>" {
		t.Errorf("linkedMessageId = %q", notes[0].LinkedMessageID)
	}
	if other := mailNotes(t, do, "inbox:2"); len(other) != 0 {
		t.Errorf("a different mail shows %d notes, want 0", len(other))
	}
}

// TestNoteLinkSurvivesAMove is the reason the link is the Message-ID. Filing the
// mail away must not detach its annotation.
func TestNoteLinkSurvivesAMove(t *testing.T) {
	do := noteLinkHarness(t)
	if rec := do(http.MethodPost, "/api/v1/notes",
		`{"title":"Chase this","linkedMessageId":"<contract-1@hermex.test>"}`); rec.Code != http.StatusOK {
		t.Fatalf("create note: status %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/api/v1/mail/move", `{"id":"inbox:1","to":"trash"}`); rec.Code != http.StatusOK {
		t.Fatalf("move: status %d body %s", rec.Code, rec.Body.String())
	}
	if notes := mailNotes(t, do, "trash:1"); len(notes) != 1 {
		t.Errorf("the moved mail has %d notes, want its annotation to have followed it", len(notes))
	}
}

// TestFreeStandingNoteIsNotOnAnyMail keeps an ordinary note off every mail.
func TestFreeStandingNoteIsNotOnAnyMail(t *testing.T) {
	do := noteLinkHarness(t)
	if rec := do(http.MethodPost, "/api/v1/notes", `{"title":"Shopping","body":"milk"}`); rec.Code != http.StatusOK {
		t.Fatalf("create note: status %d", rec.Code)
	}
	if notes := mailNotes(t, do, "inbox:1"); len(notes) != 0 {
		t.Errorf("a free-standing note showed on a mail: %+v", notes)
	}
}

// TestCreateMailNoteResolvesTheLink proves the write side matches the read side:
// the browser names the mail by its opaque id and the Message-ID is resolved here,
// so a note cannot be attached to a mail the caller cannot open.
func TestCreateMailNoteResolvesTheLink(t *testing.T) {
	do := noteLinkHarness(t)

	rec := do(http.MethodPost, "/api/v1/mail/notes", `{"id":"inbox:1","title":"Contract","body":"call legal"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body.String())
	}
	var created noteJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.LinkedMessageID != "<contract-1@hermex.test>" {
		t.Errorf("linkedMessageId = %q, want the mail's own Message-ID", created.LinkedMessageID)
	}
	if notes := mailNotes(t, do, "inbox:1"); len(notes) != 1 {
		t.Errorf("the mail has %d notes, want 1", len(notes))
	}
	if other := mailNotes(t, do, "inbox:2"); len(other) != 0 {
		t.Errorf("a different mail shows %d notes, want 0", len(other))
	}
}

// TestCreateMailNoteRefusesAnUnknownMail keeps the endpoint from writing a note
// keyed to nothing.
func TestCreateMailNoteRefusesAnUnknownMail(t *testing.T) {
	do := noteLinkHarness(t)
	if rec := do(http.MethodPost, "/api/v1/mail/notes", `{"id":"inbox:999","body":"x"}`); rec.Code == http.StatusOK {
		t.Errorf("a note was attached to a message that does not exist: %s", rec.Body.String())
	}
}

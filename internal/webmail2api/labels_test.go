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

// TestLabelsSurviveAReload is the round trip the reader depends on: labels set
// through POST /mail/labels are stored as the message's categories, so the next
// GET /mail/message must return them. The detail used to carry no labels, and a
// label added in the reader vanished on the next open. The folder list must show
// the same labels on the message's row, or the inbox hides them.
func TestLabelsSurviveAReload(t *testing.T) {
	do, id := labelsHarness(t)
	for _, want := range []string{"Work,Urgent", ""} {
		body, _ := json.Marshal(map[string]any{"id": id, "labels": splitLabels(want)})
		do(http.MethodPost, "/api/v1/mail/labels", string(body))
		if got := strings.Join(detailLabels(t, do, id), ","); got != want {
			t.Fatalf("detail labels = %q, want %q", got, want)
		}
		if got := strings.Join(listedLabels(t, do, id), ","); got != want {
			t.Fatalf("list labels = %q, want %q", got, want)
		}
	}
}

type labelsRequest func(method, path, body string) *httptest.ResponseRecorder

// labelsHarness files one inbox message for alice and returns a request helper
// signed in as her plus the message's opaque id.
func labelsHarness(t *testing.T) (labelsRequest, string) {
	t.Helper()
	mbox := t.TempDir()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: bob@hermex.test\r\nSubject: labelled\r\n\r\nhi\r\n"), time.Now(), 0)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("labels-test-secret")
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", method, path, rec.Code, rec.Body.String())
		}
		return rec
	}
	return do, messageID("inbox", info.UID)
}

// splitLabels turns a comma-joined label list into the request's array.
func splitLabels(joined string) []string {
	if joined == "" {
		return []string{}
	}
	return strings.Split(joined, ",")
}

// detailLabels reads the labels GET /mail/message returns for the message.
func detailLabels(t *testing.T, do labelsRequest, id string) []string {
	t.Helper()
	var d struct {
		Labels []string `json:"labels"`
	}
	rec := do(http.MethodGet, "/api/v1/mail/message?id="+id, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	return d.Labels
}

// listedLabels reads the labels the inbox list shows on the message's row.
func listedLabels(t *testing.T, do labelsRequest, id string) []string {
	t.Helper()
	var page struct {
		Emails []struct {
			ID     string   `json:"id"`
			Labels []string `json:"labels"`
		} `json:"emails"`
	}
	rec := do(http.MethodGet, "/api/v1/mail/inbox?pageSize=50", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(page.Emails) != 1 || page.Emails[0].ID != id {
		t.Fatalf("list = %+v, want the one message", page.Emails)
	}
	return page.Emails[0].Labels
}

package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestCustomFolderRoundTrip proves a message filed into a custom (non-built-in)
// folder is visible in that folder's list view AND can be opened by its id.
// Both paths used to resolve the folder via the well-known-slug table only, so a
// custom folder like "Projects" listed empty and its messages returned 404 on
// open. resolveFolder now falls back to the folder's display name, closing that
// gap for the list/open/action handlers alike.
func TestCustomFolderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.CreateFolder(nil, "Projects"); err != nil {
		t.Fatalf("create folder: %v", err)
	}
	cfid, ok := folderByName(st, "Projects")
	if !ok {
		t.Fatalf("custom folder not found after create")
	}
	raw := []byte("From: a@b.test\r\nSubject: filed away\r\n\r\nhi\r\n")
	if _, err := st.AppendMessage(cfid, raw, time.Now(), 0); err != nil {
		t.Fatalf("append: %v", err)
	}
	st.Close()

	secret := []byte("custom-folder-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	get := func(path string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// The list view must show the filed message (it used to return empty).
	listRec := get("/api/v1/mail/Projects")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", listRec.Code, listRec.Body.String())
	}
	var list struct {
		Emails []struct {
			ID      string `json:"id"`
			Subject string `json:"subject"`
		} `json:"emails"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Emails) != 1 {
		t.Fatalf("custom folder list returned %d messages, want 1", len(list.Emails))
	}
	if list.Emails[0].Subject != "filed away" {
		t.Errorf("listed subject = %q, want %q", list.Emails[0].Subject, "filed away")
	}

	// Opening the message by the id the list handed back must succeed (it used to
	// 404 with "unknown folder").
	openRec := get("/api/v1/mail/message?id=" + list.Emails[0].ID)
	if openRec.Code != http.StatusOK {
		t.Fatalf("open %s: %d %s", list.Emails[0].ID, openRec.Code, openRec.Body.String())
	}
}

package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestEmptyFolderMovesToTrashAndPurgesJunk proves POST /folders/{name}/empty ports
// the old webmail behaviour: emptying an ordinary folder moves its messages to
// Deleted Items, while emptying Junk (or Trash) discards them permanently.
func TestEmptyFolderMovesToTrashAndPurgesJunk(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.CreateFolder(nil, "Project"); err != nil {
		t.Fatalf("create folder: %v", err)
	}
	cfid, ok := folderByName(st, "Project")
	if !ok {
		t.Fatalf("custom folder not found after create")
	}
	raw := []byte("From: a@b.test\r\nSubject: x\r\n\r\nhi\r\n")
	_, _ = st.AppendMessage(cfid, raw, time.Now(), 0)
	_, _ = st.AppendMessage(cfid, raw, time.Now(), 0)
	_, _ = st.AppendMessage(int64(mapi.PrivateFIDJunk), raw, time.Now(), 0)
	st.Close()

	secret := []byte("empty-folder-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	post := func(name string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/folders/"+name+"/empty", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	if rec := post("Project"); rec.Code != 200 {
		t.Fatalf("empty custom: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("spam"); rec.Code != 200 {
		t.Fatalf("empty spam: %d %s", rec.Code, rec.Body.String())
	}

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	if msgs, _ := st2.ListMessages(cfid); len(msgs) != 0 {
		t.Errorf("custom folder has %d messages, want 0 (moved to trash)", len(msgs))
	}
	if msgs, _ := st2.ListMessages(int64(mapi.PrivateFIDDeletedItems)); len(msgs) != 2 {
		t.Errorf("trash has %d messages, want 2", len(msgs))
	}
	if msgs, _ := st2.ListMessages(int64(mapi.PrivateFIDJunk)); len(msgs) != 0 {
		t.Errorf("junk has %d messages, want 0 (purged)", len(msgs))
	}
}

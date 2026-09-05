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

// folderHarness returns a request helper plus the mailbox path.
func folderHarness(t *testing.T) (func(method, target, body string) *httptest.ResponseRecorder, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	secret := []byte("folder-delete-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	return func(method, target, body string) *httptest.ResponseRecorder {
		token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}, dir
}

// parentOf reads a folder's stored parent.
func parentOf(t *testing.T, mbox, name string) int64 {
	t.Helper()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if f.DisplayName != name {
			continue
		}
		if f.ParentID == nil {
			return int64(mapi.PrivateFIDIPMSubtree)
		}
		return *f.ParentID
	}
	t.Fatalf("folder %q is gone", name)
	return 0
}

// TestDeleteBuiltinFolderIsRefused is the data-loss guard. Every built-in folder
// is listed by display name, so "Inbox" resolved like any other folder and the
// delete took the folder AND every message in it.
func TestDeleteBuiltinFolderIsRefused(t *testing.T) {
	do, dir := folderHarness(t)

	rec := do(http.MethodDelete, "/api/v1/folders/Inbox", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("delete Inbox = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.ListMessages(int64(mapi.PrivateFIDInbox)); err != nil {
		t.Errorf("the Inbox did not survive the refused delete: %v", err)
	}
}

// TestDeleteFolderMovesItToTrashFirst pins the two-stage delete: the first
// delete moves the folder under Deleted Items, so the user can still get it back.
func TestDeleteFolderMovesItToTrashFirst(t *testing.T) {
	do, dir := folderHarness(t)
	if rec := do(http.MethodPost, "/api/v1/folders", `{"name":"Project X"}`); rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	rec := do(http.MethodDelete, "/api/v1/folders/Project%20X", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("first delete = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		MovedToTrash bool `json:"movedToTrash"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.MovedToTrash {
		t.Error("the first delete did not report a move")
	}
	if got, want := parentOf(t, dir, "Project X"), int64(mapi.PrivateFIDDeletedItems); got != want {
		t.Errorf("parent after the first delete = %d, want Deleted Items (%d)", got, want)
	}
}

// TestDeleteFolderFromTrashRemovesIt is the second stage, and the state upstream
// could not reach: a folder already in Deleted Items must go, not move again.
func TestDeleteFolderFromTrashRemovesIt(t *testing.T) {
	do, dir := folderHarness(t)
	if rec := do(http.MethodPost, "/api/v1/folders", `{"name":"Project X"}`); rec.Code != http.StatusOK {
		t.Fatalf("create = %d", rec.Code)
	}
	if rec := do(http.MethodDelete, "/api/v1/folders/Project%20X", ""); rec.Code != http.StatusOK {
		t.Fatalf("first delete = %d", rec.Code)
	}

	rec := do(http.MethodDelete, "/api/v1/folders/Project%20X", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("second delete = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		MovedToTrash bool `json:"movedToTrash"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.MovedToTrash {
		t.Error("the second delete moved the folder again instead of removing it")
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if f.DisplayName == "Project X" {
			t.Error("the folder survived the second delete")
		}
	}
}

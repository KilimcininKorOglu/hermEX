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

// makeFolder creates a top-level folder and returns the slug the create answered.
func makeFolder(t *testing.T, do func(string, string, string) *httptest.ResponseRecorder, name string) string {
	t.Helper()
	rec := do(http.MethodPost, "/api/v1/folders", `{"name":"`+name+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create %s = %d: %s", name, rec.Code, rec.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.ID
}

// TestDeleteBuiltinFolderIsRefused is the data-loss guard: "Inbox" once resolved
// by display name like any other folder and the delete took the folder AND every
// message in it. The display name, the slug and the Inbox's id slug must all be
// refused.
func TestDeleteBuiltinFolderIsRefused(t *testing.T) {
	do, dir := folderHarness(t)

	for _, ref := range []string{"Inbox", "inbox", "f13"} {
		if rec := do(http.MethodDelete, "/api/v1/folders/"+ref, ""); rec.Code == http.StatusOK {
			t.Errorf("delete %s = 200, want a refusal", ref)
		}
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	props, err := st.GetFolderProperties(int64(mapi.PrivateFIDInbox), mapi.PrDisplayName)
	if err != nil {
		t.Fatalf("the Inbox did not survive the refused delete: %v", err)
	}
	if name := propStr(props, mapi.PrDisplayName); name != "Inbox" {
		t.Errorf("Inbox display name after the refused delete = %q, want Inbox", name)
	}
}

// TestFolderNamesStayUniqueAmongSiblings proves the webmail folder and calendar
// endpoints keep [MS-OXCFOLD] 3.2.5.2 (sibling folders cannot have the same
// name): a second create of a top-level name, a calendar of that name and a
// rename onto it all answer 409 Conflict, and the folders keep their names.
func TestFolderNamesStayUniqueAmongSiblings(t *testing.T) {
	do, dir := folderHarness(t)
	makeFolder(t, do, "Project X")
	y := makeFolder(t, do, "Project Y")
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/folders", `{"name":"Project X"}`},
		{http.MethodPost, "/api/v1/calendar/calendars", `{"name":"Project X"}`},
		{http.MethodPut, "/api/v1/folders/" + y, `{"name":"Project X"}`},
	} {
		if rec := do(c.method, c.path, c.body); rec.Code != http.StatusConflict {
			t.Errorf("%s %s %s = %d, want 409: %s", c.method, c.path, c.body, rec.Code, rec.Body.String())
		}
	}
	for _, name := range []string{"Project X", "Project Y"} {
		if parentOf(t, dir, name) == 0 {
			t.Errorf("folder %s is gone", name)
		}
	}
}

// TestDeleteFolderTwiceOfOneName proves a folder can still be deleted when
// Deleted Items already holds a folder of its name: the user deletes "Project X",
// makes a new "Project X" and deletes that too. Both reach Deleted Items, the
// second under a name of its own, because sibling folders cannot share a name
// ([MS-OXCFOLD] 3.2.5.2).
func TestDeleteFolderTwiceOfOneName(t *testing.T) {
	do, dir := folderHarness(t)
	for round := range 2 {
		x := makeFolder(t, do, "Project X")
		if rec := do(http.MethodDelete, "/api/v1/folders/"+x, ""); rec.Code != http.StatusOK {
			t.Fatalf("round %d delete = %d: %s", round, rec.Code, rec.Body.String())
		}
	}
	trash := int64(mapi.PrivateFIDDeletedItems)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, err := st.CountChildFolders(trash)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("folders in Deleted Items = %d, want both deleted folders", n)
	}
}

// TestDeleteFolderMovesItToTrashFirst pins the two-stage delete: the first
// delete moves the folder under Deleted Items, so the user can still get it back.
func TestDeleteFolderMovesItToTrashFirst(t *testing.T) {
	do, dir := folderHarness(t)
	x := makeFolder(t, do, "Project X")

	rec := do(http.MethodDelete, "/api/v1/folders/"+x, "")
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
	x := makeFolder(t, do, "Project X")
	if rec := do(http.MethodDelete, "/api/v1/folders/"+x, ""); rec.Code != http.StatusOK {
		t.Fatalf("first delete = %d", rec.Code)
	}

	rec := do(http.MethodDelete, "/api/v1/folders/"+x, "")
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

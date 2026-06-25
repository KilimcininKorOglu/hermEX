package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// groupStoreAuth is a directory stub recording the group-management calls. alice owns
// "crew@hermex.test" and nothing else (the directory's own methods are covered in
// internal/directory).
type groupStoreAuth struct {
	owned   map[string][]directory.MListInfo // owner email -> their lists
	members []string
	setUser string
	setMems []string
}

func (a *groupStoreAuth) Authenticate(string, string) (string, bool) { return "/tmp", true }
func (a *groupStoreAuth) ListMListsOwnedBy(owner string) ([]directory.MListInfo, error) {
	return a.owned[owner], nil
}
func (a *groupStoreAuth) ListMembers(string) ([]string, error) { return a.members, nil }
func (a *groupStoreAuth) SetMembers(listname string, members []string) (bool, error) {
	a.setUser, a.setMems = listname, members
	return true, nil
}

// TestGroupsAPI proves the caller lists only the groups they own, reads and replaces
// members of an owned group, and is refused (403) on a group they do not own (the
// Broken Access Control / IDOR guard).
func TestGroupsAPI(t *testing.T) {
	auth := &groupStoreAuth{
		owned:   map[string][]directory.MListInfo{"alice@hermex.test": {{Listname: "crew@hermex.test"}}},
		members: []string{"bob@hermex.test"},
	}
	secret := []byte("groups-secret")
	srv := NewServer(auth, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
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

	// GET /groups lists only the owned group.
	rec := do(http.MethodGet, "/api/v1/groups", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get groups: %d %s", rec.Code, rec.Body.String())
	}
	var groups []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &groups)
	if len(groups) != 1 || groups[0]["address"] != "crew@hermex.test" {
		t.Errorf("get groups = %+v, want [crew]", groups)
	}

	// Members of the owned group are readable.
	if rec := do(http.MethodGet, "/api/v1/groups/members?address=crew@hermex.test", ""); rec.Code != http.StatusOK {
		t.Fatalf("get members of owned group: %d %s", rec.Code, rec.Body.String())
	}

	// IDOR guard: members of a group the caller does NOT own are refused.
	if rec := do(http.MethodGet, "/api/v1/groups/members?address=other@hermex.test", ""); rec.Code != http.StatusForbidden {
		t.Errorf("get members of non-owned group = %d, want 403", rec.Code)
	}

	// PUT replaces members of the owned group.
	if rec := do(http.MethodPut, "/api/v1/groups/members", `{"address":"crew@hermex.test","members":["x@hermex.test","y@hermex.test"]}`); rec.Code != http.StatusOK {
		t.Fatalf("put members: %d %s", rec.Code, rec.Body.String())
	}
	if auth.setUser != "crew@hermex.test" || len(auth.setMems) != 2 {
		t.Errorf("SetMembers got (%q, %v), want crew with 2 members", auth.setUser, auth.setMems)
	}

	// IDOR guard on write: cannot set members of a non-owned group.
	if rec := do(http.MethodPut, "/api/v1/groups/members", `{"address":"other@hermex.test","members":["z@hermex.test"]}`); rec.Code != http.StatusForbidden {
		t.Errorf("put members of non-owned group = %d, want 403", rec.Code)
	}
}

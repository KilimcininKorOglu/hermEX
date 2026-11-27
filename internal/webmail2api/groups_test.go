package webmail2api

import (
	"errors"
	"fmt"
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
	owned         map[string][]directory.MListInfo // owner email -> their lists
	members       []string
	setUser       string
	setMems       []string
	masteredLists map[string]bool // lists the LDAP sync owns; manual edits refused
}

func (a *groupStoreAuth) Authenticate(string, string) (string, bool) { return "/tmp", true }
func (a *groupStoreAuth) ListMListsOwnedBy(owner string) ([]directory.MListInfo, error) {
	return a.owned[owner], nil
}
func (a *groupStoreAuth) ListMembers(string) ([]string, error) { return a.members, nil }
func (a *groupStoreAuth) SetMembers(listname string, members []string) (bool, error) {
	if a.masteredLists[listname] {
		return false, directory.ErrLDAPMastered
	}
	a.setUser, a.setMems = listname, members
	return true, nil
}

// groupAccounts places the caller and the lists in their domains, so the owner
// scope check sees hermex.test lists as reachable and partner.test ones as not.
var groupAccounts = directory.StaticAccounts{
	"alice@hermex.test": {}, "crew@hermex.test": {}, "ad-grp@hermex.test": {}, "other@hermex.test": {},
	"foreign@partner.test": {},
}

// TestGroupsIgnoreAnOwnerOutsideTheListsScope seeds a stored owner row for a list
// of another organization, as one written before the owner rule would be: the
// caller neither sees the list nor reads or replaces its members.
func TestGroupsIgnoreAnOwnerOutsideTheListsScope(t *testing.T) {
	auth := &groupStoreAuth{
		owned:   map[string][]directory.MListInfo{"alice@hermex.test": {{Listname: "foreign@partner.test"}}},
		members: []string{"x@partner.test"},
	}
	secret := []byte("groups-scope-secret")
	srv := NewServer(auth, groupAccounts, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	groups := okBody[[]map[string]any](t, "get groups", do(http.MethodGet, "/api/v1/groups", ""))
	wantEq(t, "groups listed across the organization line", len(groups), 0)
	wantStatus(t, "read the foreign list's members",
		do(http.MethodGet, "/api/v1/groups/members?address=foreign@partner.test", ""), http.StatusForbidden)
	wantStatus(t, "replace the foreign list's members", do(http.MethodPut, "/api/v1/groups/members",
		`{"address":"foreign@partner.test","members":["y@partner.test"]}`), http.StatusForbidden)
	wantEq(t, "members written to the foreign list", auth.setUser, "")
}

// brokenScope is an account directory whose scope cannot be read.
type brokenScope struct{ directory.StaticAccounts }

func (brokenScope) SameScope(string, string) (bool, error) {
	return false, errors.New("scope read failed")
}

// TestGroupsReportAScopeReadFailure proves a scope that cannot be read answers the
// group listing with an error, not 200 and a list silently missing its groups.
func TestGroupsReportAScopeReadFailure(t *testing.T) {
	sink := withSink(t)
	auth := &groupStoreAuth{owned: map[string][]directory.MListInfo{"alice@hermex.test": {{Listname: "crew@hermex.test"}}}}
	secret := []byte("groups-broken-secret")
	srv := NewServer(auth, brokenScope{groupAccounts}, nil, "mail.hermex.test", secret, "", false)
	token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/groups", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	wantEq(t, "groups status when the scope cannot be read", rec.Code, http.StatusInternalServerError)
	if _, ok := sink.find("groups-list"); !ok {
		t.Error("the failed group listing was not recorded")
	}
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
	srv := NewServer(auth, groupAccounts, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
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
	groups := okBody[[]map[string]any](t, "get groups", do(http.MethodGet, "/api/v1/groups", ""))
	if len(groups) != 1 {
		t.Fatalf("get groups = %+v, want [crew]", groups)
	}
	wantEq(t, "listed group address", groups[0]["address"], any("crew@hermex.test"))

	// Members of the owned group are readable.
	wantStatus(t, "get members of owned group",
		do(http.MethodGet, "/api/v1/groups/members?address=crew@hermex.test", ""), http.StatusOK)

	// IDOR guard: members of a group the caller does NOT own are refused.
	wantStatus(t, "get members of non-owned group",
		do(http.MethodGet, "/api/v1/groups/members?address=other@hermex.test", ""), http.StatusForbidden)

	// PUT replaces members of the owned group.
	wantStatus(t, "put members", do(http.MethodPut, "/api/v1/groups/members",
		`{"address":"crew@hermex.test","members":["x@hermex.test","y@hermex.test"]}`), http.StatusOK)
	wantEq(t, "SetMembers list", auth.setUser, "crew@hermex.test")
	wantEq(t, "SetMembers member count", len(auth.setMems), 2)

	// IDOR guard on write: cannot set members of a non-owned group.
	wantStatus(t, "put members of non-owned group", do(http.MethodPut, "/api/v1/groups/members",
		`{"address":"other@hermex.test","members":["z@hermex.test"]}`), http.StatusForbidden)
	wantEq(t, "list written after the refused put", auth.setUser, "crew@hermex.test")
	wantEq(t, "members written after the refused put", strings.Join(auth.setMems, ","), "x@hermex.test,y@hermex.test")

	// A body that is no JSON, or names no list, writes nothing.
	for _, body := range []string{`{`, `{"members":["z@hermex.test"]}`} {
		if rec := do(http.MethodPut, "/api/v1/groups/members", body); rec.Code == http.StatusOK {
			t.Errorf("put %s answered 200", body)
		}
	}
	wantEq(t, "members after the malformed puts", strings.Join(auth.setMems, ","), "x@hermex.test,y@hermex.test")
}

// TestGroupsLDAPMastered proves an AD-synced (LDAP-mastered) group is flagged so the
// SPA can present it read-only, and that editing its members is refused (403) because
// the directory sync owns it.
func TestGroupsLDAPMastered(t *testing.T) {
	auth := &groupStoreAuth{
		owned: map[string][]directory.MListInfo{"alice@hermex.test": {
			{Listname: "ad-grp@hermex.test", LDAPMastered: true},
		}},
		masteredLists: map[string]bool{"ad-grp@hermex.test": true},
	}
	secret := []byte("groups-mastered-secret")
	srv := NewServer(auth, groupAccounts, nil, "mail.hermex.test", secret, "", false)
	do := func(method, target, body string) *httptest.ResponseRecorder {
		token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
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

	groups := okBody[[]map[string]any](t, "get groups", do(http.MethodGet, "/api/v1/groups", ""))
	if len(groups) != 1 || groups[0]["ldapMastered"] != true {
		t.Fatalf("get groups = %+v, want ad-grp flagged ldapMastered", groups)
	}

	if rec := do(http.MethodPut, "/api/v1/groups/members", `{"address":"ad-grp@hermex.test","members":["z@hermex.test"]}`); rec.Code != http.StatusForbidden {
		t.Errorf("put members of mastered group = %d, want 403", rec.Code)
	}
	wantEq(t, "list written for the mastered group", auth.setUser, "")
}

// vanishedListAuth owns a list the directory no longer holds when the write
// runs, as when the list is deleted between the owner check and the set.
type vanishedListAuth struct{ groupStoreAuth }

func (a *vanishedListAuth) SetMembers(string, []string) (bool, error) { return false, nil }

// TestGroupsAnswerAVanishedListAsNotFound proves a member set the directory
// reports as reaching no list answers 404, not 200 for members never stored.
func TestGroupsAnswerAVanishedListAsNotFound(t *testing.T) {
	auth := &vanishedListAuth{groupStoreAuth{owned: map[string][]directory.MListInfo{
		"alice@hermex.test": {{Listname: "crew@hermex.test"}},
	}}}
	secret := []byte("groups-vanished-secret")
	srv := NewServer(auth, groupAccounts, nil, "mail.hermex.test", secret, "", false)
	token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/groups/members",
		strings.NewReader(`{"address":"crew@hermex.test","members":["bob@hermex.test"]}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	wantStatus(t, "put members of a vanished list", rec, http.StatusNotFound)
}

// unroutableAuth refuses every member set the way the directory refuses one holding
// an address no relay can route.
type unroutableAuth struct{ groupStoreAuth }

func (a *unroutableAuth) SetMembers(string, []string) (bool, error) {
	return false, fmt.Errorf("%w: list member %q", directory.ErrUnroutableAddress, "bob@")
}

// TestGroupsRefuseAnUnroutableMemberAsTheClientsError proves a member the directory
// refuses as unroutable is answered 400 with a message the owner can act on, not
// 500 "could not save", which reads as a server fault.
func TestGroupsRefuseAnUnroutableMemberAsTheClientsError(t *testing.T) {
	auth := &unroutableAuth{groupStoreAuth{owned: map[string][]directory.MListInfo{
		"alice@hermex.test": {{Listname: "crew@hermex.test"}},
	}}}
	secret := []byte("groups-unroutable-secret")
	srv := NewServer(auth, groupAccounts, nil, "mail.hermex.test", secret, "", false)
	token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/groups/members",
		strings.NewReader(`{"address":"crew@hermex.test","members":["bob@"]}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "single email address") {
		t.Errorf("put an unroutable member = %d %s, want 400 naming the rule", rec.Code, rec.Body.String())
	}
}

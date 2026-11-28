package admin

import (
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestAdminGetUserQuota proves a system admin reads a user's quota limits and
// current usage from the mailbox store.
func TestAdminGetUserQuota(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{
		quota: map[string]objectstore.QuotaLimits{"/mb/alice": {SendKB: 1024, ReceiveKB: 2048, StorageKB: 4096}},
		used:  map[string]int64{"/mb/alice": 5 * 1024 * 1024},
	}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	unauth := authedGET(t, ts, "/admin/users/alice@hermex.test/quota", "invalid-session")
	wantStatus(t, unauth, http.StatusUnauthorized, "unauthenticated quota GET")

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/quota", session)
	body := wantBody(t, resp, http.StatusOK, "get quota")
	if !strings.Contains(body, `"receiveKB":2048`) || !strings.Contains(body, `"usedBytes":5242880`) {
		t.Errorf("quota body = %s, want the limits and usage", body)
	}
}

// TestAdminSetUserQuota proves a system admin writes a user's quota limits through
// to the mailbox store.
func TestAdminSetUserQuota(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/quota", session, csrf,
		`{"sendKB":1024,"receiveKB":2048,"storageKB":4096}`)
	wantStatus(t, resp, http.StatusNoContent, "set quota")
	if store.setQuotaDir != "/mb/alice" || store.setQuotaVal.ReceiveKB != 2048 || store.setQuotaVal.StorageKB != 4096 {
		t.Errorf("stored quota = dir %q %+v, want /mb/alice with the submitted limits", store.setQuotaDir, store.setQuotaVal)
	}
}

// TestAdminSetUserQuotaRefusesWhatIsNotAKilobyteCount sends a negative limit, one
// past 32 bits and a string. Each is a 400 that writes nothing: a limit wrapped or
// read as 0 would make the mailbox unlimited.
func TestAdminSetUserQuotaRefusesWhatIsNotAKilobyteCount(t *testing.T) {
	for _, body := range []string{`{"storageKB":-1}`, `{"receiveKB":4294967296}`, `{"sendKB":"1024"}`} {
		store := &fakeStore{}
		ts := adminServerStore(t, oofUserDir(), store)
		session, csrf := loginCookies(t, ts)
		wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/quota", session, csrf, body),
			http.StatusBadRequest, body)
		if store.setQuotaDir != "" {
			t.Errorf("%s was stored as %+v", body, store.setQuotaVal)
		}
	}
}

// TestAdminUserQuotaNotFound proves quota read/write on an unknown user is a 404
// and the store is never written.
func TestAdminUserQuotaNotFound(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}, getUserMissing: true}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/ghost@hermex.test/quota", session)
	wantStatus(t, get, http.StatusNotFound, "unknown user get")
	put := authedPUT(t, ts, "/admin/users/ghost@hermex.test/quota", session, csrf, `{"sendKB":1}`)
	wantStatus(t, put, http.StatusNotFound, "unknown user put")
	if store.setQuotaDir != "" {
		t.Errorf("an unknown user still wrote quota at %q", store.setQuotaDir)
	}
}

// TestAdminUserQuotaRequiresSystem proves a domain admin cannot read or write the
// quota of a user outside its scope through the user-scoped API, and cannot use
// the panel form, which is system-only.
func TestAdminUserQuotaRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test", Maildir: "/mb/alice"}}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/quota", session)
	wantStatus(t, get, http.StatusForbidden, "domain admin get")
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test/quota", session, csrf, `{"sendKB":1}`)
	wantStatus(t, put, http.StatusForbidden, "domain admin put")
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/quota", session, csrf,
		url.Values{"storagemb": {"999999"}}), http.StatusForbidden, "domain-admin UI quota")
	if store.setQuotaDir != "" {
		t.Errorf("a domain admin stored a quota on %q: %+v", store.setQuotaDir, store.setQuotaVal)
	}
}

// TestUIUserDetailShowsQuota proves the detail page renders the storage-quota
// section with the usage in its largest unit and the limits in MiB.
func TestUIUserDetailShowsQuota(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{
		quota: map[string]objectstore.QuotaLimits{"/mb/alice": {ReceiveKB: 2048}}, // 2 MiB
		used:  map[string]int64{"/mb/alice": 3 * 1024 * 1024},                     // 3 MiB
	}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	body := wantBody(t, resp, http.StatusOK, "detail quota")
	for _, want := range []string{"Storage quota", "Used space: 3 MB", `name="receivemb" value="2"`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page quota section missing %q", want)
		}
	}
}

// TestUIUserQuota proves the detail-form save converts the MiB inputs to KiB and
// writes them through to the store, reporting success.
func TestUIUserQuota(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/quota", session, csrf, url.Values{
		"sendmb":    {"10"},
		"receivemb": {"20"},
		"storagemb": {"30"},
	})
	body := wantBody(t, resp, http.StatusOK, "ui quota")
	got := store.setQuotaVal
	if store.setQuotaDir != "/mb/alice" || got.SendKB != 10*1024 || got.ReceiveKB != 20*1024 || got.StorageKB != 30*1024 {
		t.Errorf("stored quota = dir %q %+v, want the MiB inputs converted to KiB", store.setQuotaDir, got)
	}
	if !strings.Contains(body, "Saved") {
		t.Errorf("ui quota save did not report success:\n%s", body)
	}
}

// TestUIUserQuotaRefusesWhatIsNotASize sends quota entries a typo or a hostile form
// produces: not a number and negative were stored as 0, the unlimited quota, and
// must now be refused with nothing stored. A count past the KiB ceiling is clamped
// to it (~4 TiB), never wrapped: 2^54 MiB times 1024 overflows 64 bits.
func TestUIUserQuotaRefusesWhatIsNotASize(t *testing.T) {
	for _, bad := range []string{"12x", "-5"} {
		store := &fakeStore{}
		ts := adminServerStore(t, oofUserDir(), store)
		session, csrf := loginCookies(t, ts)
		resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/quota", session, csrf, url.Values{"storagemb": {bad}})
		body := wantBody(t, resp, http.StatusOK, "refused size")
		if store.setQuotaDir != "" {
			t.Errorf("%s: a quota was stored: %+v", bad, store.setQuotaVal)
		}
		if !strings.Contains(body, "whole number of MB") {
			t.Errorf("%s: the panel does not name the refusal:\n%s", bad, body)
		}
	}
	store := &fakeStore{}
	ts := adminServerStore(t, oofUserDir(), store)
	session, csrf := loginCookies(t, ts)
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/quota", session, csrf, url.Values{"storagemb": {"18014398509481984"}}),
		http.StatusOK, "huge quota")
	if store.setQuotaVal.StorageKB != math.MaxUint32/1024*1024 {
		t.Errorf("a huge quota stored %d KiB, want the ceiling %d", store.setQuotaVal.StorageKB, math.MaxUint32/1024*1024)
	}
}

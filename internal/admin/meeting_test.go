package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestAdminGetUserMeeting proves a system admin reads a user's automatic
// meeting-processing settings.
func TestAdminGetUserMeeting(t *testing.T) {
	store := &fakeStore{meetingConfig: map[string]objectstore.MeetingConfig{
		"/mb/alice": {AutoAccept: true, DeclineConflict: true},
	}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/meeting", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get meeting status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"autoAccept":true`) || !strings.Contains(string(body), `"declineRecurring":false`) || !strings.Contains(string(body), `"declineConflict":true`) {
		t.Errorf("meeting body = %s, want the config", body)
	}
}

// TestAdminSetUserMeeting proves a system admin writes the meeting-processing config.
func TestAdminSetUserMeeting(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/meeting", session, csrf,
		`{"autoAccept":true,"declineRecurring":true,"declineConflict":false}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set meeting status %d, want 204", resp.StatusCode)
	}
	want := objectstore.MeetingConfig{AutoAccept: true, DeclineRecurring: true, DeclineConflict: false}
	if store.setMeetingDir != "/mb/alice" || store.setMeetingConfig != want {
		t.Errorf("stored meeting = %q/%+v, want /mb/alice/%+v", store.setMeetingDir, store.setMeetingConfig, want)
	}
}

// TestAdminUserMeetingRequiresSystem proves a domain admin cannot read or write a
// user's meeting settings through the system-scoped endpoints.
func TestAdminUserMeetingRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/meeting", session)
	get.Body.Close()
	put := authedPUT(t, ts, "/admin/users/alice@hermex.test/meeting", session, csrf, `{"autoAccept":true}`)
	put.Body.Close()
	if get.StatusCode != http.StatusForbidden || put.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin meeting = GET %d / PUT %d, want both 403", get.StatusCode, put.StatusCode)
	}
}

// TestUIUserDetailShowsMeeting proves the detail page renders the meeting section with
// the stored checkboxes pre-checked.
func TestUIUserDetailShowsMeeting(t *testing.T) {
	store := &fakeStore{meetingConfig: map[string]objectstore.MeetingConfig{
		"/mb/alice": {AutoAccept: true},
	}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"Automatic processing of meeting requests", `name="autoaccept" checked`, `name="declinerecurring"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page meeting section missing %q", want)
		}
	}
}

// TestUIUserMeeting proves the detail-form save writes the meeting config from the
// checkboxes and reports success.
func TestUIUserMeeting(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/meeting", session, csrf, url.Values{
		"autoaccept":      {"on"},
		"declineconflict": {"on"},
		// declinerecurring omitted → unchecked
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui meeting save status %d, want 200", resp.StatusCode)
	}
	want := objectstore.MeetingConfig{AutoAccept: true, DeclineRecurring: false, DeclineConflict: true}
	if store.setMeetingConfig != want {
		t.Errorf("stored meeting = %+v, want %+v", store.setMeetingConfig, want)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("ui meeting save did not report success:\n%s", body)
	}
}

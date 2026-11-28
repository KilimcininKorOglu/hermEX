package admin

import (
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

	body := wantBody(t, authedGET(t, ts, "/admin/users/alice@hermex.test/meeting", session), http.StatusOK, "get meeting")
	if !strings.Contains(body, `"autoAccept":true`) || !strings.Contains(body, `"declineRecurring":false`) || !strings.Contains(body, `"declineConflict":true`) {
		t.Errorf("meeting body = %s, want the config", body)
	}
}

// TestAdminSetUserMeeting proves a system admin writes the meeting-processing config.
func TestAdminSetUserMeeting(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/meeting", session, csrf,
		`{"autoAccept":true,"declineRecurring":true,"declineConflict":false}`), http.StatusNoContent, "set meeting")
	want := objectstore.MeetingConfig{AutoAccept: true, DeclineRecurring: true, DeclineConflict: false}
	if store.setMeetingDir != "/mb/alice" || store.setMeetingConfig != want {
		t.Errorf("stored meeting = %q/%+v, want /mb/alice/%+v", store.setMeetingDir, store.setMeetingConfig, want)
	}
}

// TestAdminUserMeetingRequiresSystem proves a domain admin cannot read or write the
// meeting settings of a user outside its scope through the user-scoped API, and
// cannot use the panel form, which is system-only.
func TestAdminUserMeetingRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test", Maildir: "/mb/alice"}}
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	unauthResp, err := http.Get(ts.URL + "/admin/users/alice@hermex.test/meeting")
	mustNoErr(t, err, "unauth meeting request")
	wantStatus(t, unauthResp, http.StatusUnauthorized, "unauth meeting")

	wantStatus(t, authedGET(t, ts, "/admin/users/alice@hermex.test/meeting", session), http.StatusForbidden, "domain admin GET meeting")
	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/meeting", session, csrf, `{"autoAccept":true}`), http.StatusForbidden, "domain admin PUT meeting")
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/meeting", session, csrf,
		url.Values{"autoaccept": {"on"}}), http.StatusForbidden, "domain-admin UI meeting")
	if store.setMeetingDir != "" {
		t.Errorf("a domain admin wrote meeting settings to %q: %+v", store.setMeetingDir, store.setMeetingConfig)
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

	body := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session), http.StatusOK, "detail page meeting")
	for _, want := range []string{"Meeting requests", `name="autoaccept" checked`, `name="declinerecurring"`, `name="removerequest"`,
		`name="processcancellations" checked`} {
		if !strings.Contains(body, want) {
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

	body := wantBody(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/meeting", session, csrf, url.Values{
		"autoaccept":      {"on"},
		"declineconflict": {"on"},
		"removerequest":   {"on"},
		// declinerecurring omitted → unchecked
	}), http.StatusOK, "ui meeting save")
	want := objectstore.MeetingConfig{
		AutoAccept: true, DeclineRecurring: false, DeclineConflict: true, RemoveRequestOnResponse: true,
		LeaveCancellationsUnprocessed: true, // processcancellations omitted, so unchecked
	}
	if store.setMeetingConfig != want {
		t.Errorf("stored meeting = %+v, want %+v", store.setMeetingConfig, want)
	}
	if !strings.Contains(body, "Saved") {
		t.Errorf("ui meeting save did not report success:\n%s", body)
	}
}

// TestUIUserMeetingProcessesCancellations proves a checked cancellation box stores
// the default, processing cancellations, and the JSON API carries the setting.
func TestUIUserMeetingProcessesCancellations(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/meeting", session, csrf, url.Values{"processcancellations": {"on"}}),
		http.StatusOK, "ui meeting process cancellations")
	// The zero config is also what an unwritten store reads, so the write must be seen.
	if store.setMeetingDir != "/mb/alice" {
		t.Errorf("the save wrote to %q, want alice's mailbox written", store.setMeetingDir)
	}
	if store.setMeetingConfig != (objectstore.MeetingConfig{}) {
		t.Errorf("stored meeting = %+v, want the zero config", store.setMeetingConfig)
	}
	wantStatus(t, authedPUT(t, ts, "/admin/users/alice@hermex.test/meeting", session, csrf, `{"leaveCancellationsUnprocessed":true}`),
		http.StatusNoContent, "JSON PUT meeting")
	if !store.setMeetingConfig.LeaveCancellationsUnprocessed {
		t.Errorf("JSON PUT stored %+v, want cancellations left unprocessed", store.setMeetingConfig)
	}
}

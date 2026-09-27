package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// errReadFailed is the failure a read method returns when a test names it in
// fakeDir.readErrs.
var errReadFailed = errors.New("directory unreachable")

// TestAFailedReadHidesTheFormItFeeds proves a page does not offer a form built
// from a read that failed. That form showed empty or default values, and saving it
// replaced what is stored, so the page reports the failure in the form's place.
func TestAFailedReadHidesTheFormItFeeds(t *testing.T) {
	for _, tc := range []struct {
		read, path, form, what string
	}{
		{"GetDefaultSyncPolicy", "/admin/ui/syncpolicy", `hx-put="/admin/ui/syncpolicy"`, "the default device policy"},
		{"GetCreateDefaults", "/admin/ui/defaults", `hx-put="/admin/ui/defaults"`, "the create defaults"},
		{"EffectiveUserDefaults", "/admin/ui/defaults", `hx-put="/admin/ui/defaults"`, "the create defaults"},
		{"GetTLSSettings", "/admin/ui/tls", `hx-post="/admin/ui/tls/mode"`, "the certificate mode"},
		{"GetMTASTSSettings", "/admin/ui/tls", `hx-post="/admin/ui/mtasts"`, "the MTA-STS settings"},
		{"ListOrgs", "/admin/ui/domains/1", `hx-put="/admin/ui/domains/1"`, "the organizations"},
		{"ListUsersInDomain", "/admin/ui/domains/1", `/admin/ui/domains/1/catchall"`, "the users of this domain"},
		{"GetDomainCatchAll", "/admin/ui/domains/1", `/admin/ui/domains/1/catchall"`, "the catch-all mailbox"},
		{"GetDomainSpamThreshold", "/admin/ui/domains/1", `/admin/ui/domains/1/spam-threshold"`, "the spam threshold"},
		{"GetDomainAVScan", "/admin/ui/domains/1", `/admin/ui/domains/1/avscan"`, "the antivirus settings"},
		{"SplitRelayHost", "/admin/ui/domains/1", `/admin/ui/domains/1/split"`, "the split domain host"},
		{"GetDomainNameTemplates", "/admin/ui/domains/1", `/admin/ui/domains/1/sendername"`, "the outgoing display name templates"},
		{"GetDomainSyncPolicy", "/admin/ui/domains/1", `/admin/ui/domains/1/syncpolicy"`, "the device policy of this domain"},
		{"GetCreateDefaults", "/admin/ui/domains/1", `/admin/ui/domains/1/createdefaults"`, "the create defaults override"},
		{"GetDomainBranding", "/admin/ui/domains/1", `/admin/ui/domains/1/branding"`, "the login branding"},
	} {
		d := &fakeDir{
			authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
			readErrs:     map[string]error{tc.read: errReadFailed},
		}
		ts := adminServer(t, d)
		session, _ := loginCookies(t, ts)

		page := wantBody(t, authedGET(t, ts, tc.path, session), http.StatusOK, tc.path)
		wantContains(t, page, "Could not read "+tc.what, tc.read+": the failed read is reported")
		if strings.Contains(page, tc.form) {
			t.Errorf("%s: %s offers the form after the read failed", tc.read, tc.path)
		}
	}
}

// userDetailReadFailure is a signed-in system administrator's server whose directory
// and mailbox store both fail the named read, with bob@acme.test as the user whose
// detail page is under test. The read fails from after the sign-in, which itself
// reads the administrator's roles.
func userDetailReadFailure(t *testing.T, read string) (*httptest.Server, string, string) {
	t.Helper()
	errs := map[string]error{}
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{ID: 8, Username: "bob@acme.test", Maildir: "/data/bob"},
		readErrs:   errs,
	}
	ts := adminServerStore(t, d, &fakeStore{readErrs: errs})
	session, csrf := loginCookies(t, ts)
	errs[read] = errReadFailed
	return ts, session, csrf
}

// TestAUserDetailReadHidesTheFormItFeeds proves the user detail page offers no form
// built from a read that failed. The grant lists rendered empty, and saving one
// revoked every grant it did not show; the other forms replaced their stored value
// with an empty or default one.
func TestAUserDetailReadHidesTheFormItFeeds(t *testing.T) {
	for _, tc := range []struct {
		read, form, what string
	}{
		{"ListAltnames", `/altnames"`, "the alternative login names"},
		{"ListAliasesFor", "Save aliases", "the aliases"},
		{"GetForward", `/forward"`, "the forward"},
		{"GetUserProperties", `/contact"`, "the user properties"},
		{"GetUserProperties", `/hide"`, "the user properties"},
		{"GetUserSpamThreshold", `/spam-threshold"`, "the spam threshold"},
		{"GetOOFSettings", `/oof"`, "the out-of-office settings"},
		{"GetQuota", `/quota"`, "the quota"},
		{"GetMeetingConfig", `/meeting"`, "the meeting settings"},
		{"GetSentCopyConfig", `/sentcopy"`, "the sent-copy settings"},
		{"GetSyncPolicy", "Save device policy", "the device policy of this user"},
		{"GetDelegates", `/delegates"`, "the delegates"},
		{"GetStoreOwners", `/storeowners"`, "the store owners"},
		{"GetSendAs", `/sendas"`, "the send-as grants"},
		{"GetSendOnBehalf", `/sendonbehalf"`, "the send-on-behalf grants"},
	} {
		ts, session, _ := userDetailReadFailure(t, tc.read)
		page := wantBody(t, authedGET(t, ts, "/admin/ui/users/bob@acme.test", session), http.StatusOK, tc.read)
		wantContains(t, page, "Could not read "+tc.what, tc.read+": the failed read is reported")
		if strings.Contains(page, tc.form) {
			t.Errorf("%s: the user page offers the %s form after the read failed", tc.read, tc.form)
		}
	}
}

// TestAUserDetailListReadIsNotShownAsEmpty proves the user detail lists report a
// read that failed, on the page and on the panel a change re-renders. They rendered
// as no devices, no remote accounts, no folders and no admin roles.
func TestAUserDetailListReadIsNotShownAsEmpty(t *testing.T) {
	const user = "/admin/ui/users/bob@acme.test"
	for _, tc := range []struct {
		read, reported, absent, panel string
		form                          url.Values
	}{
		{"ListDevices", "Could not read the mobile devices.", "No mobile devices.", user + "/devices/action", url.Values{"deviceID": {"phone"}, "action": {"resync"}}},
		{"ListFetchmail", "Could not read the remote accounts.", "No remote accounts.", user + "/fetchmail", url.Values{}},
		{"AdminRoles", "Could not read the admin roles.", "No admin roles.", user + "/roles/grant", url.Values{"role": {"system"}}},
		{"ListFolders", "Could not read the folders.", "Select a folder", "", nil},
	} {
		ts, session, csrf := userDetailReadFailure(t, tc.read)
		bodies := map[string]string{"page": wantBody(t, authedGET(t, ts, user, session), http.StatusOK, tc.read)}
		if tc.panel != "" {
			bodies["panel"] = wantBody(t, htmxPOST(t, ts, tc.panel, session, csrf, tc.form), http.StatusOK, tc.panel)
		}
		for name, body := range bodies {
			wantContains(t, body, tc.reported, tc.read+" "+name+": the failed read is reported")
			if strings.Contains(body, tc.absent) {
				t.Errorf("%s %s: still renders %q after the read failed", tc.read, name, tc.absent)
			}
		}
	}
}

// TestADomainDetailListReadIsNotShownAsEmpty proves the domain detail page reports
// a list it could not read. The members lists rendered as "No users." and the like,
// and the DNS records named no DKIM key or left out the MTA-STS records.
func TestADomainDetailListReadIsNotShownAsEmpty(t *testing.T) {
	for _, tc := range []struct {
		read, reported, absent string
	}{
		{"ListUsersInDomain", "Could not read the users of this domain.", "No users."},
		{"ListContactsInDomain", "Could not read the contacts of this domain.", "No contacts."},
		{"ListMListsInDomain", "Could not read the groups of this domain.", "No groups."},
		{"GetMTASTSSettings", "Could not read the MTA-STS settings, so the required records", `class="dns-label"`},
		{"GetDKIMKeyInfo", "Could not read the DKIM key, so the required records", `class="dns-label"`},
	} {
		d := &fakeDir{
			authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
			readErrs:     map[string]error{tc.read: errReadFailed},
		}
		ts := adminServer(t, d)
		session, _ := loginCookies(t, ts)

		page := wantBody(t, authedGET(t, ts, "/admin/ui/domains/1", session), http.StatusOK, tc.read)
		wantContains(t, page, tc.reported, tc.read+": the failed read is reported")
		if strings.Contains(page, tc.absent) {
			t.Errorf("%s: the page still renders %q after the read failed", tc.read, tc.absent)
		}
	}
}

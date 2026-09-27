package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
		{"GetLDAPConfig", "/admin/ui/ldap", `hx-post="/admin/ui/ldap"`, "the directory configuration"},
		{"ListDomains", "/admin/ui/ldap", `hx-post="/admin/ui/ldap"`, "the domains"},
		{"ListOrgs", "/admin/ui/roles/1", `hx-put="/admin/ui/roles/1"`, "the organizations"},
		{"ListDomains", "/admin/ui/roles/1", `hx-put="/admin/ui/roles/1"`, "the domains"},
		{"ListUsers", "/admin/ui/roles/1", `hx-put="/admin/ui/roles/1"`, "the users"},
		{"ListMembers", "/admin/ui/mlists/team@acme.test", `/members"`, "the members"},
		{"ListSpecifieds", "/admin/ui/mlists/team@acme.test", `/specifieds"`, "the permitted senders"},
		{"GetAntispamSettings", "/admin/ui/antispam", `hx-post="/admin/ui/antispam/settings"`, "the scoring settings"},
		{"GetAntispamSettings", "/admin/ui/antispam", "the verdict gains", "the scoring settings"},
		{"GetGreylistEnabled", "/admin/ui/antispam", `hx-post="/admin/ui/antispam/greylist"`, "the greylisting switch"},
		{"GetGreylistEnabled", "/admin/ui/antispam", "Greylisting is", "the greylisting switch"},
		{"GetGreylistTimings", "/admin/ui/antispam", `/antispam/greylist-timings"`, "the greylist timings"},
		{"GetRateLimitSettings", "/admin/ui/antispam", `/antispam/ratelimit"`, "the rate-limit settings"},
		{"GetRateLimitSettings", "/admin/ui/antispam", "Rate limiting is", "the rate-limit settings"},
		{"GetMessageSizeSettings", "/admin/ui/antispam", `/antispam/message-size"`, "the message size limit"},
		{"GetOutboundSettings", "/admin/ui/antispam", `/antispam/outbound"`, "the outbound settings"},
		{"GetOutboundSettings", "/admin/ui/antispam", "Outbound abuse limiting is", "the outbound settings"},
		{"GetRelaySettings", "/admin/ui/antispam", `/antispam/relay"`, "the retry settings"},
		{"GetDigestSettings", "/admin/ui/antispam", `/antispam/digest"`, "the digest settings"},
		{"GetDigestSettings", "/admin/ui/antispam", "The quarantine digest is", "the digest settings"},
		{"GetAutoReplySettings", "/admin/ui/settings", `/antispam/autoreply"`, "the auto-reply settings"},
		{"GetOutboundSettings", "/admin/ui/settings", `/antispam/outbound"`, "the outbound settings"},
		{"GetSizeLimits", "/admin/ui/limits", `hx-post="/admin/ui/limits"`, "the size limits"},
		{"GetHTTPRateLimitSettings", "/admin/ui/limits", `/limits/requestrate"`, "the request-rate settings"},
		{"GetHTTPRateLimitSettings", "/admin/ui/limits", "Request rate limiting is", "the request-rate settings"},
		{"GetConnLimitSettings", "/admin/ui/limits", `/limits/connections"`, "the connection caps"},
		{"GetConnLimitSettings", "/admin/ui/limits", "The connection cap is", "the connection caps"},
		{"GetLoginLockoutSettings", "/admin/ui/limits", `/limits/loginlockout"`, "the login-lockout settings"},
		{"GetLoginLockoutSettings", "/admin/ui/limits", "A login that fails", "the login-lockout settings"},
		{"GetFetchSettings", "/admin/ui/limits", `/limits/fetchpolicy"`, "the fetch policy"},
		{"GetSizeLimits", "/admin/ui/settings", `hx-post="/admin/ui/limits"`, "the size limits"},
		{"GetLoginLockoutSettings", "/admin/ui/settings", `/limits/loginlockout"`, "the login-lockout settings"},
		{"GetLogRetentionDays", "/admin/ui/settings", `hx-post="/admin/ui/log-retention"`, "the log retention"},
		{"GetRecoverableSettings", "/admin/ui/settings", `hx-post="/admin/ui/recoverable-retention"`, "the Recoverable Items retention"},
		{"GetSpamHistorySettings", "/admin/ui/settings", `/spam-history/retention"`, "the spam history retention"},
		{"GetSpamHistorySettings", "/admin/ui/spam-history", `/spam-history/retention"`, "the spam history retention"},
		{"GetSpamHistorySettings", "/admin/ui/spam-history", "scored verdicts are kept", "the spam history retention"},
	} {
		d := &fakeDir{
			authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
			namedRoles:   map[int64]directory.RoleDetail{1: roleDetail(1, "Helpdesk", "", nil, nil)},
			mlists:       []directory.MListInfo{{Listname: "team@acme.test"}},
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

// TestAnUnreadableModelOrRulesetIsReported proves the anti-spam page reports a
// model or ruleset file it could not read. The page showed an unreadable model as
// the cold-start model and an unreadable ruleset as the embedded baseline, which
// only a missing file means.
func TestAnUnreadableModelOrRulesetIsReported(t *testing.T) {
	paths := fakePaths{root: t.TempDir()}
	for _, p := range []string{paths.AntispamModelPath(), paths.AntispamRulesPath()} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	srv := NewServer(d, paths, []byte("test-secret"))
	srv.store = &fakeStore{}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	session, _ := loginCookies(t, ts)

	page := wantBody(t, authedGET(t, ts, "/admin/ui/antispam", session), http.StatusOK, "antispam page")
	wantContains(t, page, "Could not read the trained model.", "the unreadable model is reported")
	wantContains(t, page, "Could not read the ruleset in data_dir.", "the unreadable ruleset is reported")
	for _, absent := range []string{"cold-start model", "embedded baseline"} {
		if strings.Contains(page, absent) {
			t.Errorf("an unreadable file still reads as %q", absent)
		}
	}
}

// TestAFailedListReadIsNotAMissingMailingList proves the mailing list page answers
// a failed read of the lists as a server error. It answered 404, which tells the
// operator the list does not exist.
func TestAFailedListReadIsNotAMissingMailingList(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		mlists:   []directory.MListInfo{{Listname: "team@acme.test"}},
		readErrs: map[string]error{"ListMLists": errReadFailed},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/mlists/team@acme.test", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status %d after a failed read, want 500", resp.StatusCode)
	}
}

// TestAFailedListReadIsNotShownAsEmpty proves a list page reports a list it could
// not read, on the page and on the panel its create form re-renders. The table
// read as empty, which says the directory holds none.
func TestAFailedListReadIsNotShownAsEmpty(t *testing.T) {
	for _, tc := range []struct {
		read, path, reported, absent string
	}{
		{"ListAliases", "/admin/ui/aliases", "Could not read the aliases.", "No aliases yet."},
		{"ListContacts", "/admin/ui/contacts", "Could not read the contacts.", "No contacts yet."},
		{"ListAllRooms", "/admin/ui/rooms", "Could not read the rooms.", "No rooms yet."},
		{"ListMLists", "/admin/ui/mlists", "Could not read the mailing lists.", "No mailing lists yet."},
		{"ListOrgs", "/admin/ui/orgs", "Could not read the organizations.", "No organizations yet."},
		{"ListRoles", "/admin/ui/roles", "Could not read the roles.", "No roles yet."},
		{"ListDomains", "/admin/ui/domains", "Could not read the domains.", "No domains yet."},
		{"ListUsers", "/admin/ui/users", "Could not read the users.", "No users yet."},
	} {
		d := &fakeDir{
			authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			readErrs: map[string]error{tc.read: errReadFailed},
		}
		ts := adminServer(t, d)
		session, csrf := loginCookies(t, ts)

		bodies := map[string]string{
			"page":  wantBody(t, authedGET(t, ts, tc.path, session), http.StatusOK, tc.path),
			"panel": wantBody(t, htmxPOST(t, ts, tc.path, session, csrf, url.Values{}), http.StatusOK, tc.path),
		}
		for name, body := range bodies {
			wantContains(t, body, tc.reported, tc.read+" "+name+": the failed read is reported")
			if strings.Contains(body, tc.absent) {
				t.Errorf("%s %s: still renders %q after the read failed", tc.read, name, tc.absent)
			}
		}
	}
}

// TestAFailedChoiceReadHidesTheCreateForm proves a create form whose choices could
// not be read is not offered. It rendered with no choice to pick, which reads as
// the directory holding none.
func TestAFailedChoiceReadHidesTheCreateForm(t *testing.T) {
	for _, tc := range []struct {
		read, path, form, reported string
	}{
		{"ListDomains", "/admin/ui/contacts", `hx-post="/admin/ui/contacts"`, "Could not read the domains."},
		{"GetCreateDefaults", "/admin/ui/domains", `hx-post="/admin/ui/domains"`, "Could not read the create defaults."},
		{"ListDomains", "/admin/ui/users", `hx-post="/admin/ui/users"`, "Could not read the domains."},
		{"EffectiveUserDefaults", "/admin/ui/users", `hx-post="/admin/ui/users"`, "Could not read the create defaults."},
	} {
		d := &fakeDir{
			authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			readErrs: map[string]error{tc.read: errReadFailed},
		}
		ts := adminServer(t, d)
		session, _ := loginCookies(t, ts)

		page := wantBody(t, authedGET(t, ts, tc.path, session), http.StatusOK, tc.path)
		wantContains(t, page, tc.reported, tc.read+": the failed read is reported")
		if strings.Contains(page, tc.form) {
			t.Errorf("%s: %s offers the create form after the read failed", tc.read, tc.path)
		}
	}
}

// TestAFailedDefaultsReadRefusesTheUserCreate proves a new-user form whose domain
// defaults could not be read creates no user. Picking a domain re-fetched the
// fields, and a failed read returned them empty, so the user was created with no
// service and no quota.
func TestAFailedDefaultsReadRefusesTheUserCreate(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
		readErrs:     map[string]error{"EffectiveUserDefaults": errReadFailed},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	fields := wantBody(t, authedGET(t, ts, "/admin/ui/user-create-fields?domain=1", session), http.StatusOK, "fields")
	wantContains(t, fields, "Could not read the create defaults.", "fields: the failed read is reported")
	wantContains(t, fields, `name="defaults_unread"`, "fields: the form is marked")
	if strings.Contains(fields, `name="lang"`) {
		t.Errorf("fields: the failed read still offers the fields:\n%s", fields)
	}

	panel := wantBody(t, htmxPOST(t, ts, "/admin/ui/users", session, csrf, url.Values{
		"local": {"new"}, "domain": {"1"}, "password": {"pw"}, "defaults_unread": {"1"},
	}), http.StatusOK, "create")
	wantContains(t, panel, "no user was created", "create: the refusal is reported")
	if d.createdUser != "" {
		t.Errorf("created %q from a form whose defaults could not be read", d.createdUser)
	}
}

// TestADomainListOrganizationReadIsNotShownAsNone proves the domains list reports a
// failed read of the organizations, on the page and on the panel a create
// re-renders. A domain in an organization read "None", which says it is in none.
func TestADomainListOrganizationReadIsNotShownAsNone(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domains:  []directory.DomainInfo{{ID: 1, Name: "acme.test", OrgID: 3}},
		readErrs: map[string]error{"ListOrgs": errReadFailed},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	bodies := map[string]string{
		"page":  wantBody(t, authedGET(t, ts, "/admin/ui/domains", session), http.StatusOK, "domains page"),
		"panel": wantBody(t, htmxPOST(t, ts, "/admin/ui/domains", session, csrf, url.Values{}), http.StatusOK, "create"),
	}
	for name, body := range bodies {
		wantContains(t, body, "Could not read the organizations.", name+": the failed read is reported")
		wantContains(t, body, `<span class="muted">Unknown</span>`, name+": the organization is unknown")
		if strings.Contains(body, `<span class="muted">None</span>`) {
			t.Errorf("%s: a domain in an organization reads as in none after the read failed", name)
		}
	}
}

// TestAnOrganizationDomainReadIsNotShownAsNone proves the organization page reports
// a failed read of the domains, on the page and on the panel a change re-renders.
// The table read "No domains in this organization." and the add form offered no
// domain.
func TestAnOrganizationDomainReadIsNotShownAsNone(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		orgs:     map[int64]directory.OrgInfo{1: {ID: 1, Name: "Acme"}},
		readErrs: map[string]error{"ListDomains": errReadFailed},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	bodies := map[string]string{
		"page":  wantBody(t, authedGET(t, ts, "/admin/ui/orgs/1", session), http.StatusOK, "org page"),
		"panel": wantBody(t, htmxPOST(t, ts, "/admin/ui/orgs/1/domains", session, csrf, url.Values{"domainID": {"x"}}), http.StatusOK, "attach"),
	}
	for name, body := range bodies {
		wantContains(t, body, "Could not read the domains.", name+": the failed read is reported")
		if strings.Contains(body, "No domains in this organization.") || strings.Contains(body, `/orgs/1/domains"`) {
			t.Errorf("%s: a failed read renders as an organization with no domains:\n%s", name, body)
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

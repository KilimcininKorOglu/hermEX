package admin

import (
	"errors"
	"net/http"
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

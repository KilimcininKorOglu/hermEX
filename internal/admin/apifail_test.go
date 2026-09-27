package admin

import (
	"net/http"
	"testing"

	"hermex/internal/directory"
)

// TestAFailedReadIsRecordedByTheJSONAPI proves the admin JSON API records the error
// behind a 500 it answers. The handlers wrote a bare "server error" and recorded
// nothing, so the operator's log had no trace of the failed directory or mailbox
// read. The directory and the mailbox store fakes share one error map, so a read
// fails whichever of the two serves it.
func TestAFailedReadIsRecordedByTheJSONAPI(t *testing.T) {
	const user = "/admin/users/bob@acme.test"
	for _, tc := range []struct {
		path, read string
	}{
		{"/admin/domains", "ListDomains"},
		{"/admin/domains/1", "GetDomain"},
		{"/admin/domains/1/syncpolicy", "GetDomainSyncPolicy"},
		{"/admin/domains/1/createdefaults", "GetCreateDefaults"},
		{"/admin/users", "ListUsers"},
		{user, "GetUser"},
		{user + "/altnames", "ListAltnames"},
		{user + "/aliases", "ListAliasesFor"},
		{user + "/forward", "GetForward"},
		{user + "/sendas", "GetSendAs"},
		{user + "/sentcopy", "GetSentCopyConfig"},
		{user + "/meeting", "GetMeetingConfig"},
		{user + "/storeowners", "GetStoreOwners"},
		{user + "/syncpolicy", "GetSyncPolicy"},
		{user + "/fetchmail", "ListFetchmail"},
		{user + "/contact", "GetUserProperties"},
		{user + "/oof", "GetOOFSettings"},
		{user + "/devices", "ListDevices"},
		{user + "/quota", "GetQuota"},
		{"/admin/syncpolicy", "GetDefaultSyncPolicy"},
		{"/admin/tasq/status", "ListTasks"},
		{"/admin/mobile-devices", "ListActiveSessions"},
		{"/admin/aliases", "ListAliases"},
		{"/admin/orgs", "ListOrgs"},
		{"/admin/orgs/0/ldap", "GetLDAPConfig"},
		{"/admin/roles", "ListRoles"},
		{"/admin/roles/1", "GetRole"},
	} {
		errs := map[string]error{}
		d := &fakeDir{
			authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
			userDetail:   directory.UserDetail{ID: 8, Username: "bob@acme.test", Maildir: "/data/bob"},
			readErrs:     errs,
		}
		ts, sink := loggingAdminServerStore(t, d, &fakeStore{readErrs: errs})
		session, _ := loginCookies(t, ts)
		errs[tc.read] = errReadFailed

		name := tc.path + " " + tc.read
		resp := authedGET(t, ts, tc.path, session)
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s: status %d, want 500", name, resp.StatusCode)
			continue
		}
		if e, ok := sink.find("request.fail"); !ok || e.Err != errReadFailed.Error() {
			t.Errorf("%s: the failed read was not recorded (event %+v)", name, e)
		}
	}
}

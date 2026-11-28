package admin

import (
	"net/http"
	"net/url"
	"testing"

	"hermex/internal/directory"
)

// TestDomainSaveAuditsAnUnreadablePriorValue proves a domain setting save whose
// prior value could not be read records that in the audit entry. The entry
// claimed the antivirus toggles were off, or no catch-all and no split host were
// set, which is a prior state nobody read.
func TestDomainSaveAuditsAnUnreadablePriorValue(t *testing.T) {
	for _, tc := range []struct {
		read, path string
		form       url.Values
		fields     []string
	}{
		{"GetDomainAVScan", "/admin/ui/domains/1/avscan", url.Values{"av_scan_inbound": {"on"}}, []string{"old_inbound", "old_outbound"}},
		{"GetDomainCatchAll", "/admin/ui/domains/1/catchall", url.Values{"catchall": {""}}, []string{"old"}},
		{"SplitRelayHost", "/admin/ui/domains/1/split", url.Values{"split_relay_host": {""}}, []string{"old_host"}},
	} {
		d := &fakeDir{
			authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
			readErrs:     map[string]error{tc.read: errReadFailed},
		}
		ts, sink := loggingAdminServer(t, d)
		session, csrf := loginCookies(t, ts)
		wantStatus(t, htmxPUT(t, ts, tc.path, session, csrf, tc.form), http.StatusOK, tc.path)

		e, ok := sink.find("setting.change")
		if !ok {
			t.Fatalf("%s: the save recorded no setting.change event", tc.path)
		}
		for _, field := range tc.fields {
			if e.Fields[field] != "unreadable" {
				t.Errorf("%s: %s = %v, want unreadable", tc.path, field, e.Fields[field])
			}
		}
	}
}

// TestDomainSaveAuditsTheReadPriorValue is the control: a prior value that was
// read is recorded as that value, so "unreadable" above marks only a failed read.
func TestDomainSaveAuditsTheReadPriorValue(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
		avInbound:    true,
	}
	ts, sink := loggingAdminServer(t, d)
	session, csrf := loginCookies(t, ts)
	wantStatus(t, htmxPUT(t, ts, "/admin/ui/domains/1/avscan", session, csrf, url.Values{}), http.StatusOK, "avscan PUT")

	e, ok := sink.find("setting.change")
	if !ok {
		t.Fatal("the save recorded no setting.change event")
	}
	if e.Fields["old_inbound"] != true || e.Fields["old_outbound"] != false {
		t.Errorf("prior values = %v / %v, want true / false", e.Fields["old_inbound"], e.Fields["old_outbound"])
	}
}

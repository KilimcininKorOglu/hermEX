package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/tlsrpt"
)

// reportsDir is a directory holding one report of each kind in each of two
// domains, mine.test (id 1) and theirs.test (id 2), for an admin with the given
// roles.
func reportsDir(roles ...directory.AdminRole) *fakeDir {
	return &fakeDir{
		authOK: true, uid: 7, roles: roles,
		domains: []directory.DomainInfo{{ID: 1, Name: "mine.test"}, {ID: 2, Name: "theirs.test"}},
		reports: fakeReports{
			dmarc: []directory.DMARCReport{
				{ReportListing: directory.ReportListing{ID: 1, DomainID: 1, Domain: "mine.test", OrgName: "Reporter", ReportID: "mine-dmarc"},
					Records: []directory.DMARCRecordRow{{SourceIP: "192.0.2.7", Count: 3, DKIM: "pass", SPF: "fail"}}},
				{ReportListing: directory.ReportListing{ID: 2, DomainID: 2, Domain: "theirs.test", OrgName: "Reporter", ReportID: "their-dmarc"}},
			},
			tls: []directory.TLSReport{
				{ReportListing: directory.ReportListing{ID: 1, DomainID: 1, Domain: "mine.test", ReportID: "mine-tls"},
					Policies: []directory.TLSPolicyRow{{PolicyType: "sts", PolicyDomain: "mine.test", Failure: 2,
						FailureDetails: []tlsrpt.FailureDetail{{ResultType: "certificate-expired", FailedSessionCount: 2}}}}},
				{ReportListing: directory.ReportListing{ID: 2, DomainID: 2, Domain: "theirs.test", ReportID: "their-tls"}},
			},
			failures: []directory.DMARCFailure{
				{ID: 1, DomainID: 1, Domain: "mine.test", SourceIP: "198.51.100.4", OriginalHeaders: "Subject: mine-failure"},
				{ID: 2, DomainID: 2, Domain: "theirs.test", SourceIP: "203.0.113.9", OriginalHeaders: "Subject: their-failure"},
			},
		},
	}
}

func getPage(t *testing.T, ts *httptest.Server, path, session string) (int, string) {
	t.Helper()
	resp := authedGET(t, ts, path, session)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// TestUIReportsDomainAdminSeesOnlyTheirDomains is the scope rule: every tab asks
// the directory for the admin's own domains only, so another tenant's reports,
// and its name in the domain filter, never reach the page.
func TestUIReportsDomainAdminSeesOnlyTheirDomains(t *testing.T) {
	d := reportsDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	for kind, mine := range map[string]string{"dmarc": "mine-dmarc", "tlsrpt": "mine-tls", "failure": "198.51.100.4"} {
		status, page := getPage(t, ts, "/admin/ui/reports?kind="+kind, session)
		if status != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", kind, status)
		}
		if !strings.Contains(page, mine) {
			t.Errorf("%s: the page lacks the admin's own report %q", kind, mine)
		}
		for _, theirs := range []string{"their-dmarc", "their-tls", "203.0.113.9", "theirs.test"} {
			if strings.Contains(page, theirs) {
				t.Errorf("%s: the page shows another domain's %q", kind, theirs)
			}
		}
		if f := d.reports.lastFilter; f.All || !slices.Equal(f.DomainIDs, []int64{1}) {
			t.Errorf("%s: filter = %+v, want domain 1 only", kind, f)
		}
		if strings.Contains(page, "report-retention-panel") {
			t.Errorf("%s: the retention form is shown to a domain administrator", kind)
		}
	}
}

// TestUIReportsDomainFilterOutsideScopeShowsNothing: a domain id typed into the
// query must not widen the scope; naming another tenant's domain selects nothing.
func TestUIReportsDomainFilterOutsideScopeShowsNothing(t *testing.T) {
	d := reportsDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	status, page := getPage(t, ts, "/admin/ui/reports?kind=dmarc&domain=2", session)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if strings.Contains(page, "their-dmarc") || strings.Contains(page, "mine-dmarc") {
		t.Errorf("a domain filter outside the scope listed reports:\n%s", page)
	}
	if f := d.reports.lastFilter; f.All || len(f.DomainIDs) != 0 {
		t.Errorf("filter = %+v, want an empty scope", f)
	}
}

// TestUIReportsFilterNarrowsAndBoundsThePeriod: a system administrator's domain
// filter narrows to that domain, and the inclusive end date becomes the start of
// the next day.
func TestUIReportsFilterNarrowsAndBoundsThePeriod(t *testing.T) {
	d := reportsDir(directory.AdminRole{Role: directory.AdminSystem})
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	status, _ := getPage(t, ts, "/admin/ui/reports?kind=dmarc&domain=2&from=2025-09-01&to=2025-09-30", session)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	f := d.reports.lastFilter
	if f.All || !slices.Equal(f.DomainIDs, []int64{2}) {
		t.Errorf("filter scope = %+v, want domain 2 only", f)
	}
	if f.From != 1756684800 || f.To != 1759276800 {
		t.Errorf("period = [%d, %d), want [1756684800, 1759276800)", f.From, f.To)
	}
}

// TestUIReportDetailOutsideScopeIs404 is the IDOR guard: a detail id is a
// sequence number anyone can guess, so each detail page checks the report's
// domain and answers another tenant's report exactly like a missing one.
func TestUIReportDetailOutsideScopeIs404(t *testing.T) {
	d := reportsDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	for kind, mine := range map[string]string{"dmarc": "192.0.2.7", "tlsrpt": "certificate-expired", "failure": "Subject: mine-failure"} {
		status, page := getPage(t, ts, "/admin/ui/reports/"+kind+"/1", session)
		if status != http.StatusOK || !strings.Contains(page, mine) {
			t.Errorf("%s/1: status %d, want 200 showing %q", kind, status, mine)
		}
		status, page = getPage(t, ts, "/admin/ui/reports/"+kind+"/2", session)
		if status != http.StatusNotFound {
			t.Errorf("%s/2: status %d, want 404", kind, status)
		}
		if strings.Contains(page, "theirs.test") {
			t.Errorf("%s/2: the answer names the other domain", kind)
		}
		if status, _ = getPage(t, ts, "/admin/ui/reports/"+kind+"/99", session); status != http.StatusNotFound {
			t.Errorf("%s/99: status %d, want 404", kind, status)
		}
	}
}

// TestUIReportsSummaryRenders: a system administrator's page carries the summary
// totals above the list, and the retention form.
func TestUIReportsSummaryRenders(t *testing.T) {
	d := reportsDir(directory.AdminRole{Role: directory.AdminSystem})
	d.reports.dmarcSummary = []directory.DMARCSourceSummary{{Domain: "mine.test", HeaderFrom: "mine.test", SourceIP: "192.0.2.77", Messages: 41, DMARCPass: 40}}
	d.reports.tlsSummary = []directory.TLSPolicySummary{{Domain: "mine.test", PolicyDomain: "mx-policy.mine.test", Success: 12, Failure: 5}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	for kind, want := range map[string][]string{
		"dmarc":  {"192.0.2.77", "<td>41</td>", "mine-dmarc", "their-dmarc"},
		"tlsrpt": {"mx-policy.mine.test", "<td>5</td>", "mine-tls", "their-tls"},
	} {
		status, page := getPage(t, ts, "/admin/ui/reports?kind="+kind, session)
		if status != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", kind, status)
		}
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Errorf("%s: the page lacks %q", kind, w)
			}
		}
		if !strings.Contains(page, `name="aggregate_days" value="180"`) {
			t.Errorf("%s: the retention form does not show the default window", kind)
		}
	}
}

// TestUIReportsWarnWhenPostmasterCannotReceive: a report is stored only for mail
// delivered to a postmaster recipient, so the page names each domain whose
// postmaster address is refused at RCPT or expands to a list's members. An
// address that resolves, directly or through the catch-all, raises no warning.
func TestUIReportsWarnWhenPostmasterCannotReceive(t *testing.T) {
	d := reportsDir(directory.AdminRole{Role: directory.AdminSystem})
	d.domains = []directory.DomainInfo{
		{ID: 1, Name: "alias.test"}, {ID: 2, Name: "catch.test"},
		{ID: 3, Name: "list.test"}, {ID: 4, Name: "none.test"},
	}
	d.resolvable = map[string]bool{"postmaster@alias.test": true}
	d.catchAll = map[string]string{"catch.test": "box@catch.test"}
	d.mlists = []directory.MListInfo{{ID: 1, Listname: "Postmaster@List.test"}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	status, page := getPage(t, ts, "/admin/ui/reports", session)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(page, "postmaster@list.test is a distribution list") {
		t.Error("the page does not warn about the list postmaster")
	}
	if !strings.Contains(page, "postmaster@none.test does not resolve") {
		t.Error("the page does not warn about the unresolvable postmaster")
	}
	for _, fine := range []string{"postmaster@alias.test", "postmaster@catch.test"} {
		if strings.Contains(page, fine) {
			t.Errorf("the page warns about %s, which receives reports", fine)
		}
	}
}

// TestUIReportRetentionRequiresSystem: the retention window deletes every
// domain's reports, so only a full system administrator may change it.
func TestUIReportRetentionRequiresSystem(t *testing.T) {
	form := url.Values{"aggregate_days": {"90"}, "failure_days": {"7"}}

	scoped := reportsDir(directory.AdminRole{Role: directory.AdminDomain, ScopeID: 1})
	ts := adminServer(t, scoped)
	session, csrf := loginCookies(t, ts)
	resp := htmxPOST(t, ts, "/admin/ui/reports/retention", session, csrf, form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || scoped.reports.settingsFound {
		t.Errorf("domain admin: status %d, stored %v; want 403 and nothing stored", resp.StatusCode, scoped.reports.settingsFound)
	}

	system := reportsDir(directory.AdminRole{Role: directory.AdminSystem})
	ts = adminServer(t, system)
	session, csrf = loginCookies(t, ts)
	resp = htmxPOST(t, ts, "/admin/ui/reports/retention", session, csrf, form)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("system admin: status %d, want 200", resp.StatusCode)
	}
	if want := (directory.MailReportSettings{AggregateDays: 90, FailureDays: 7}); system.reports.settings != want {
		t.Errorf("stored %+v, want %+v", system.reports.settings, want)
	}
	if !strings.Contains(string(body), `name="failure_days" value="7"`) {
		t.Errorf("the panel does not show the saved window:\n%s", body)
	}
}

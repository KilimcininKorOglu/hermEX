package admin

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"hermex/internal/directory"
)

// reportKinds are the report page's tabs: DMARC aggregate, TLS and DMARC failure
// reports. The first is the default.
var reportKinds = []string{"dmarc", "tlsrpt", "failure"}

const (
	reportDateLayout = "2006-01-02"
	reportTimeLayout = "2006-01-02 15:04 UTC"
	reportListLimit  = 200
)

// reportListView is one aggregate or TLS report formatted for the list.
type reportListView struct {
	ID       int64
	Domain   string
	OrgName  string
	ReportID string
	Period   string
	Received string
	Total    int64
	Failed   int64
}

// failureListView is one DMARC failure report formatted for the list.
type failureListView struct {
	ID               int64
	Domain           string
	Received         string
	SourceIP         string
	AuthFailure      string
	OriginalMailFrom string
	DKIMDomain       string
}

// unixUTC formats a unix time for the report pages; zero reads as unknown.
func unixUTC(sec int64) string {
	if sec == 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(reportTimeLayout)
}

// reportPeriod formats a report's period.
func reportPeriod(begin, end int64) string {
	return unixUTC(begin) + " to " + unixUTC(end)
}

// handleUIReports renders the report page: one tab per report kind, filtered by
// domain and period, with a summary above the list. A system administrator sees
// every domain's reports, a domain administrator only their domains'.
func (s *Server) handleUIReports(w http.ResponseWriter, r *http.Request) {
	all, ids, ok := s.uiScopedDomainsPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	if !slices.Contains(reportKinds, kind) {
		kind = reportKinds[0]
	}
	filter, problems := reportFilterFrom(q, all, ids)
	data := map[string]any{
		"Nav": "reports", "Kind": kind, "CSRF": csrfCookieValue(r),
		"DomainID": q.Get("domain"), "From": q.Get("from"), "To": q.Get("to"),
	}
	domains, err := s.reportDomains(all, ids)
	if err != nil {
		problems = append(problems, s.notice("Could not read the domains.", err))
	}
	data["Domains"] = domains
	if err == nil {
		warnings, werr := s.postmasterWarnings(domains)
		if werr != nil {
			problems = append(problems, s.notice("Could not check the postmaster addresses.", werr))
		}
		data["PostmasterWarnings"] = warnings
	}
	if err := s.fillReports(data, kind, filter); err != nil {
		problems = append(problems, s.notice("Could not read the reports.", err))
	}
	if cl, ok := s.uiClaims(r); ok && s.isSystemAdmin(cl.UserID) {
		data["CanEdit"] = true
		s.fillMailReportRetention(data)
	}
	data["Error"] = strings.Join(problems, " ")
	s.render(w, "reports.html", data)
}

// reportFilterFrom builds the directory filter from the page's query: the
// caller's scope, narrowed to one domain when the domain filter names one inside
// it, and the period. The "to" date is inclusive. A domain outside the scope
// selects nothing. It returns a notice for each value it could not read.
func reportFilterFrom(q url.Values, all bool, ids map[int64]bool) (directory.ReportFilter, []string) {
	f := directory.ReportFilter{All: all, DomainIDs: domainIDList(ids), Limit: reportListLimit}
	var problems []string
	if v := q.Get("domain"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		switch {
		case err != nil:
			problems = append(problems, "The domain filter is not valid.")
		case all || ids[id]:
			f.All, f.DomainIDs = false, []int64{id}
		default:
			f.All, f.DomainIDs = false, nil
		}
	}
	from, ok := parseReportDate(q.Get("from"))
	if !ok {
		problems = append(problems, "The start date is not valid.")
	}
	to, ok := parseReportDate(q.Get("to"))
	if !ok {
		problems = append(problems, "The end date is not valid.")
	}
	if to > 0 {
		to += int64((24 * time.Hour).Seconds())
	}
	f.From, f.To = from, to
	return f, problems
}

// parseReportDate reads a YYYY-MM-DD date as the unix time of its start in UTC.
// An empty value is no bound (0); ok is false for a value that is not a date.
func parseReportDate(v string) (int64, bool) {
	if v == "" {
		return 0, true
	}
	t, err := time.Parse(reportDateLayout, v)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}

// reportDomains lists the domains the caller may filter by.
func (s *Server) reportDomains(all bool, ids map[int64]bool) ([]directory.DomainInfo, error) {
	domains, err := s.dir.ListDomains()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(domains, func(d directory.DomainInfo) bool { return !all && !ids[d.ID] }), nil
}

// postmasterWarning names a domain whose postmaster address cannot receive a
// report that gets stored.
type postmasterWarning struct {
	Domain string
	// List reports that the address is a distribution list. The list delivers to
	// its members, so the message never reaches a postmaster recipient and the
	// report in it is not stored.
	List bool
}

// postmasterWarnings checks postmaster@<domain> for each domain in the order
// RCPT resolves it: a distribution list first, then a mailbox, alias or
// alternate name, then the domain's catch-all. A report is stored only for mail
// delivered to a postmaster recipient, so an address that resolves to none of
// them is refused at RCPT and the report never arrives.
func (s *Server) postmasterWarnings(domains []directory.DomainInfo) ([]postmasterWarning, error) {
	lists, err := s.dir.ListMLists()
	if err != nil {
		return nil, err
	}
	isList := make(map[string]bool, len(lists))
	for _, l := range lists {
		isList[strings.ToLower(l.Listname)] = true
	}
	var out []postmasterWarning
	for _, d := range domains {
		addr := "postmaster@" + strings.ToLower(d.Name)
		if isList[addr] {
			out = append(out, postmasterWarning{Domain: d.Name, List: true})
			continue
		}
		if _, ok := s.dir.Resolve(addr); ok {
			continue
		}
		_, catchAll, err := s.dir.GetDomainCatchAll(d.Name)
		if err != nil {
			return nil, err
		}
		if !catchAll {
			out = append(out, postmasterWarning{Domain: d.Name})
		}
	}
	return out, nil
}

// fillReports reads one tab's summary and list into the page data.
func (s *Server) fillReports(data map[string]any, kind string, f directory.ReportFilter) error {
	switch kind {
	case "tlsrpt":
		summary, err := s.dir.TLSSummary(f)
		if err != nil {
			return err
		}
		list, err := s.dir.ListTLSReports(f)
		data["TLSSummary"], data["Reports"] = summary, reportListViews(list)
		return err
	case "failure":
		list, err := s.dir.ListDMARCFailures(f)
		data["Failures"] = failureListViews(list)
		return err
	default:
		summary, err := s.dir.DMARCSummary(f)
		if err != nil {
			return err
		}
		list, err := s.dir.ListDMARCReports(f)
		data["DMARCSummary"], data["Reports"] = summary, reportListViews(list)
		return err
	}
}

func reportListViews(list []directory.ReportListing) []reportListView {
	out := make([]reportListView, 0, len(list))
	for _, l := range list {
		out = append(out, reportListView{
			ID: l.ID, Domain: l.Domain, OrgName: l.OrgName, ReportID: l.ReportID,
			Period: reportPeriod(l.Begin, l.End), Received: unixUTC(l.ReceivedAt),
			Total: l.Total, Failed: l.Failed,
		})
	}
	return out
}

func failureListViews(list []directory.DMARCFailure) []failureListView {
	out := make([]failureListView, 0, len(list))
	for _, f := range list {
		out = append(out, failureListView{
			ID: f.ID, Domain: f.Domain, Received: unixUTC(f.ReceivedAt), SourceIP: f.SourceIP,
			AuthFailure: f.AuthFailure, OriginalMailFrom: f.OriginalMailFrom, DKIMDomain: f.DKIMDomain,
		})
	}
	return out
}

// reportDetailID gates a report detail page and parses its {id}. It returns the
// caller's read scope for the visibility check; ok=false means a response was
// already written.
func (s *Server) reportDetailID(w http.ResponseWriter, r *http.Request) (id int64, all bool, ids map[int64]bool, ok bool) {
	all, ids, ok = s.uiScopedDomainsPage(w, r)
	if !ok {
		return 0, false, nil, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "no such report", http.StatusNotFound)
		return 0, false, nil, false
	}
	return id, all, ids, true
}

// reportVisible answers a detail lookup that did not produce a visible report. A
// report of a domain outside the caller's scope answers the same 404 as a missing
// one, so a domain administrator cannot learn which ids exist elsewhere.
func (s *Server) reportVisible(w http.ResponseWriter, found bool, err error, inScope bool) bool {
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return false
	}
	if !found || !inScope {
		http.Error(w, "no such report", http.StatusNotFound)
		return false
	}
	return true
}

// handleUIDMARCReport renders one aggregate report with its records.
func (s *Server) handleUIDMARCReport(w http.ResponseWriter, r *http.Request) {
	id, all, ids, ok := s.reportDetailID(w, r)
	if !ok {
		return
	}
	rep, found, err := s.dir.GetDMARCReport(id)
	if !s.reportVisible(w, found, err, all || ids[rep.DomainID]) {
		return
	}
	s.render(w, "report-dmarc.html", map[string]any{
		"Nav": "reports", "CSRF": csrfCookieValue(r), "R": rep,
		"Period": reportPeriod(rep.Begin, rep.End), "Received": unixUTC(rep.ReceivedAt),
	})
}

// handleUITLSReport renders one TLS report with its policies.
func (s *Server) handleUITLSReport(w http.ResponseWriter, r *http.Request) {
	id, all, ids, ok := s.reportDetailID(w, r)
	if !ok {
		return
	}
	rep, found, err := s.dir.GetTLSReport(id)
	if !s.reportVisible(w, found, err, all || ids[rep.DomainID]) {
		return
	}
	s.render(w, "report-tlsrpt.html", map[string]any{
		"Nav": "reports", "CSRF": csrfCookieValue(r), "R": rep,
		"Period": reportPeriod(rep.Begin, rep.End), "Received": unixUTC(rep.ReceivedAt),
	})
}

// handleUIDMARCFailure renders one DMARC failure report.
func (s *Server) handleUIDMARCFailure(w http.ResponseWriter, r *http.Request) {
	id, all, ids, ok := s.reportDetailID(w, r)
	if !ok {
		return
	}
	rep, found, err := s.dir.GetDMARCFailure(id)
	if !s.reportVisible(w, found, err, all || ids[rep.DomainID]) {
		return
	}
	s.render(w, "report-failure.html", map[string]any{
		"Nav": "reports", "CSRF": csrfCookieValue(r), "R": rep,
		"Arrival": unixUTC(rep.ArrivalDate), "Received": unixUTC(rep.ReceivedAt),
	})
}

// fillMailReportRetention sets the stored retention windows, or the defaults
// when none has been saved, on a page-data map.
func (s *Server) fillMailReportRetention(data map[string]any) {
	rs := directory.MailReportSettings{
		AggregateDays: directory.DefaultMailReportAggregateDays,
		FailureDays:   directory.DefaultMailReportFailureDays,
	}
	stored, found, err := s.dir.GetMailReportSettings()
	switch {
	case err != nil:
		data["Notice"] = s.notice("Could not read the retention setting; the defaults are shown.", err)
	case found:
		rs = stored
	}
	data["AggregateDays"], data["FailureDays"] = rs.AggregateDays, rs.FailureDays
}

// handleUISaveMailReportRetention persists the report retention windows, in whole
// days. The admin sweep deletes expired reports to match within about a minute,
// no restart. A value of zero or less keeps that kind of report forever; formInt
// already maps a blank or negative entry to zero.
func (s *Server) handleUISaveMailReportRetention(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	rs := directory.MailReportSettings{
		AggregateDays: formInt(r, "aggregate_days"),
		FailureDays:   formInt(r, "failure_days"),
	}
	data := map[string]any{"CSRF": csrfCookieValue(r)}
	if err := s.dir.SetMailReportSettings(rs); err != nil {
		s.fillMailReportRetention(data)
		data["Notice"] = s.notice("Could not save the retention setting.", err)
		s.render(w, "report-retention-panel", data)
		return
	}
	s.fillMailReportRetention(data)
	data["Notice"] = "Report retention saved; the sweep deletes expired reports within a minute, no restart."
	s.render(w, "report-retention-panel", data)
}

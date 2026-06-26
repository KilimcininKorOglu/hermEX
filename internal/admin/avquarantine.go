package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// avQuarantineView is one quarantine record formatted for the panel.
type avQuarantineView struct {
	When       string
	Direction  string
	From       string
	Recipients string
	Subject    string
	Virus      string
	Status     string
}

// uiScopedDomainsPage gates a domain-scoped UI page: no session redirects to
// login, and a caller with no readable domain gets 403. It returns the caller's
// read scope (all=true for a system admin, otherwise the specific domain ids) so
// the handler can filter rows. ok=false means a response was already written.
func (s *Server) uiScopedDomainsPage(w http.ResponseWriter, r *http.Request) (all bool, ids map[int64]bool, ok bool) {
	cl, authed := s.uiClaims(r)
	if !authed {
		http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
		return false, nil, false
	}
	all, ids = s.scopedReadDomains(cl.UserID)
	if !all && len(ids) == 0 {
		http.Error(w, "forbidden: requires a domain administrator", http.StatusForbidden)
		return false, nil, false
	}
	return all, ids, true
}

// handleUIAVQuarantine renders the antivirus quarantine page. A system admin sees
// every held message; a domain admin sees only their domains' (scopedReadDomains
// drives the row filter).
func (s *Server) handleUIAVQuarantine(w http.ResponseWriter, r *http.Request) {
	all, ids, ok := s.uiScopedDomainsPage(w, r)
	if !ok {
		return
	}
	recs, err := s.dir.ListQuarantine(domainIDList(ids), all, 200)
	errMsg := ""
	if err != nil {
		errMsg = "Could not read the quarantine: " + err.Error()
	}
	views := make([]avQuarantineView, 0, len(recs))
	for _, rec := range recs {
		views = append(views, avQuarantineView{
			When:       time.Unix(rec.CreatedAt, 0).UTC().Format("2006-01-02 15:04 UTC"),
			Direction:  rec.Direction,
			From:       rec.MailFrom,
			Recipients: strings.Join(rec.Recipients, ", "),
			Subject:    rec.Subject,
			Virus:      rec.VirusName,
			Status:     rec.Status,
		})
	}
	s.render(w, "avquarantine.html", map[string]any{
		"Nav": "avquarantine", "Items": views, "Error": errMsg,
	})
}

// domainIDList flattens a scopedReadDomains id set into a slice for ListQuarantine.
func domainIDList(ids map[int64]bool) []int64 {
	out := make([]int64, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	return out
}

// handleUISaveDomainAVScan stores a domain's antivirus inbound/outbound scan
// toggles from the domain-detail form (a system administrator action, like the
// domain's other settings), returning the shared save-status partial for htmx.
func (s *Server) handleUISaveDomainAVScan(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	data := map[string]any{}
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		data["Error"] = "Invalid domain id."
		s.render(w, "user-status", data)
		return
	}
	dd, found, err := s.dir.GetDomain(id)
	switch {
	case err != nil:
		data["Error"] = "Server error."
	case !found:
		data["Error"] = "No such domain."
	default:
		inbound := r.PostFormValue("av_scan_inbound") == "on"
		outbound := r.PostFormValue("av_scan_outbound") == "on"
		if err := s.dir.SetDomainAVScan(dd.Name, inbound, outbound); err != nil {
			data["Error"] = "Could not save the antivirus toggles: " + err.Error()
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}

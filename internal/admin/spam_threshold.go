package admin

import (
	"net/http"
	"strconv"
	"strings"
)

// spamThresholdFromForm reads a per-scope spam-threshold override from the form. An
// empty field clears the override (the scope inherits); a value below 1 is invalid
// (it would flag every message as spam). It returns (threshold, ok): ok=false means
// the value was rejected.
func spamThresholdFromForm(r *http.Request) (threshold *int, ok bool) {
	raw := strings.TrimSpace(r.FormValue("spam_threshold"))
	if raw == "" {
		return nil, true // inherit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return nil, false
	}
	return &n, true
}

// handleUIUserSpamThreshold sets or clears a user's spam-threshold override from the
// detail form and returns the refreshed status panel; an empty field clears it so the
// user inherits the domain or global threshold.
func (s *Server) handleUIUserSpamThreshold(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	data := map[string]any{}
	th, valid := spamThresholdFromForm(r)
	switch {
	case !valid:
		data["Error"] = "Threshold must be at least 1, or empty to inherit."
	default:
		if err := s.dir.SetUserSpamThreshold(r.PathValue("email"), th); err != nil {
			data["Error"] = "Could not save threshold: " + err.Error()
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}

// handleUIDomainSpamThreshold sets or clears a domain's spam-threshold override from
// the detail form and returns the refreshed status panel; an empty field clears it so
// the domain inherits the global threshold.
func (s *Server) handleUIDomainSpamThreshold(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	data := map[string]any{}
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	dd, found, derr := s.dir.GetDomain(id)
	th, valid := spamThresholdFromForm(r)
	switch {
	case derr != nil:
		data["Error"] = "Server error."
	case !found:
		data["Error"] = "No such domain."
	case !valid:
		data["Error"] = "Threshold must be at least 1, or empty to inherit."
	default:
		if err := s.dir.SetDomainSpamThreshold(dd.Name, th); err != nil {
			data["Error"] = "Could not save threshold: " + err.Error()
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}

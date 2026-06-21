package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/easpolicy"
)

// policyFieldView is one policy field rendered for the editor: its name, whether it is
// a boolean toggle (else a numeric limit), and its current value ("" = inherit).
type policyFieldView struct {
	Name  string
	Bool  bool
	Value string
}

// policyView projects a policy onto the canonical ordered field list, marking each
// field's current value or blank when it is unset (inherited).
func policyView(p easpolicy.Policy) []policyFieldView {
	out := make([]policyFieldView, 0, len(easpolicy.Fields))
	for _, f := range easpolicy.Fields {
		v := ""
		if val, ok := p[f.Name]; ok {
			v = strconv.Itoa(val)
		}
		out = append(out, policyFieldView{Name: f.Name, Bool: f.Kind == easpolicy.Bool, Value: v})
	}
	return out
}

// policyFromForm reads a submitted policy editor: a field left blank is omitted (it
// inherits the layer below), a field with a value is enforced at that integer. An
// unparseable or out-of-range value is reported rather than silently dropped, so a
// crafted form post cannot bypass the same range check the JSON API applies.
func policyFromForm(r *http.Request) (easpolicy.Policy, error) {
	p := easpolicy.Policy{}
	for _, f := range easpolicy.Fields {
		v := strings.TrimSpace(r.PostFormValue(f.Name))
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a number", f.Name, v)
		}
		p[f.Name] = n
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// handleGetUserSyncPolicy returns a user's per-user device-policy override (system
// administrators only); an unset field is simply absent and inherits the default.
func (s *Server) handleGetUserSyncPolicy(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	p, err := s.store.GetSyncPolicy(maildir)
	if err != nil {
		http.Error(w, "could not read sync policy", http.StatusInternalServerError)
		return
	}
	if p == nil {
		p = easpolicy.Policy{}
	}
	writeJSON(w, p)
}

// handleSetUserSyncPolicy replaces a user's per-user device-policy override (system
// administrators only). An unknown field is refused so it cannot be stored and then
// dropped at provisioning.
func (s *Server) handleSetUserSyncPolicy(w http.ResponseWriter, r *http.Request) {
	maildir, ok := s.resolveMaildir(w, r)
	if !ok {
		return
	}
	var in easpolicy.Policy
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := in.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.SetSyncPolicy(maildir, in); err != nil {
		http.Error(w, "could not set sync policy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUIUserSyncPolicy saves a user's device-policy override from the detail form and
// returns the refreshed status panel.
func (s *Server) handleUIUserSyncPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Server error."
	case !ok:
		data["Error"] = "No such user."
	default:
		p, perr := policyFromForm(r)
		switch {
		case perr != nil:
			data["Error"] = "Invalid value: " + perr.Error()
		default:
			if err := s.store.SetSyncPolicy(u.Maildir, p); err != nil {
				data["Error"] = "Could not save sync policy: " + err.Error()
			} else {
				data["Saved"] = true
			}
		}
	}
	s.render(w, "user-status", data)
}

// handleGetDefaultSyncPolicy returns the server-wide default device policy (system
// administrators only).
func (s *Server) handleGetDefaultSyncPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.dir.GetDefaultSyncPolicy()
	if err != nil {
		http.Error(w, "could not read default sync policy", http.StatusInternalServerError)
		return
	}
	if p == nil {
		p = easpolicy.Policy{}
	}
	writeJSON(w, p)
}

// handleSetDefaultSyncPolicy replaces the server-wide default device policy (system
// administrators only).
func (s *Server) handleSetDefaultSyncPolicy(w http.ResponseWriter, r *http.Request) {
	var in easpolicy.Policy
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := in.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.dir.SetDefaultSyncPolicy(in); err != nil {
		http.Error(w, "could not set default sync policy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetDomainSyncPolicy returns a domain's device-policy override. The route
// carries the domain id; the directory stores the policy by name, so the domain is
// resolved first (an unknown id is 404).
func (s *Server) handleGetDomainSyncPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	dd, found, err := s.dir.GetDomain(id)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	p, err := s.dir.GetDomainSyncPolicy(dd.Name)
	if err != nil {
		http.Error(w, "could not read sync policy", http.StatusInternalServerError)
		return
	}
	if p == nil {
		p = easpolicy.Policy{}
	}
	writeJSON(w, p)
}

// handleSetDomainSyncPolicy replaces a domain's device-policy override. An unknown
// field is refused so it cannot be stored and then dropped at provisioning.
func (s *Server) handleSetDomainSyncPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	dd, found, err := s.dir.GetDomain(id)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	var in easpolicy.Policy
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := in.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.dir.SetDomainSyncPolicy(dd.Name, in); err != nil {
		http.Error(w, "could not set sync policy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUISaveDomainSyncPolicy saves a domain's device-policy override from the
// domain detail form and returns the refreshed status panel.
func (s *Server) handleUISaveDomainSyncPolicy(w http.ResponseWriter, r *http.Request) {
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
		p, perr := policyFromForm(r)
		switch {
		case perr != nil:
			data["Error"] = "Invalid value: " + perr.Error()
		default:
			if _, err := s.dir.SetDomainSyncPolicy(dd.Name, p); err != nil {
				data["Error"] = "Could not save sync policy: " + err.Error()
			} else {
				data["Saved"] = true
			}
		}
	}
	s.render(w, "user-status", data)
}

// handleUISyncPolicy renders the server-default device-policy editor page.
func (s *Server) handleUISyncPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	p, _ := s.dir.GetDefaultSyncPolicy()
	s.render(w, "syncpolicy.html", map[string]any{
		"Nav":    "syncpolicy",
		"CSRF":   csrfCookieValue(r),
		"Fields": policyView(p),
	})
}

// handleUISaveSyncPolicy saves the server-default device policy from the editor and
// returns the refreshed status panel.
func (s *Server) handleUISaveSyncPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	p, err := policyFromForm(r)
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Invalid value: " + err.Error()
	default:
		if err := s.dir.SetDefaultSyncPolicy(p); err != nil {
			data["Error"] = "Could not save: " + err.Error()
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}

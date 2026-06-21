package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"hermex/internal/directory"
)

// claimsOf returns the session claims a protected handler runs under.
func claimsOf(r *http.Request) claims {
	cl, _ := r.Context().Value(ctxKey{}).(claims)
	return cl
}

// isSystemAdmin reports whether a user holds full (write) system authority,
// resolved through the single permission path: a SystemAdmin permission, whether
// granted by a named role or bridged from a direct system tier grant. A
// read-only system administrator is NOT a full administrator.
func (s *Server) isSystemAdmin(userID int64) bool {
	return hasPerm(s.adminPerms(userID), directory.PermSystemAdmin, "")
}

// requireSystem gates a handler on system authority, method-aware: a read
// (GET/HEAD) admits a read-only system administrator, while a state-changing
// method requires a full system administrator. This is the chokepoint that makes
// SystemAdminRO read-everything-write-nothing — it cannot be forgotten per
// handler.
func (s *Server) requireSystem(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := claimsOf(r).UserID
		if isReadMethod(r.Method) {
			if !s.isSystemReadAdmin(uid) {
				http.Error(w, "forbidden: requires a system administrator", http.StatusForbidden)
				return
			}
		} else if !s.isSystemAdmin(uid) {
			http.Error(w, "forbidden: requires a full system administrator", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// handleListDomains lists the domains the caller may read: a system read admin
// (or a domain admin over all) sees every domain, a scoped domain admin only its
// own.
func (s *Server) handleListDomains(w http.ResponseWriter, r *http.Request) {
	domains, err := s.dir.ListDomains()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if all, ids := s.scopedReadDomains(claimsOf(r).UserID); !all {
		var scoped []directory.DomainInfo
		for _, d := range domains {
			if ids[d.ID] {
				scoped = append(scoped, d)
			}
		}
		domains = scoped
	}
	writeJSON(w, domains)
}

// handleGetDomain returns one domain's full record, including its user counts. It
// is readable by any administrator of that domain (a system read admin, or a
// domain admin — read-only or full — over it), so the gate matches the list
// page's per-domain read scope rather than requiring full system authority.
func (s *Server) handleGetDomain(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	if !domainReadAllowed(s.adminPerms(claimsOf(r).UserID), id) {
		http.Error(w, "forbidden: requires an administrator of this domain", http.StatusForbidden)
		return
	}
	dd, ok, err := s.dir.GetDomain(id)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	writeJSON(w, dd)
}

// handleUpdateDomain edits a domain's status, mailbox cap, and contact fields
// (full system administrators only — its requireSystem wrapper enforces this;
// the reference grants OrgAdmin, but hermEX's OrgAdmin is intentionally narrow,
// so domain edit stays system-only). Unspecified fields keep their current value
// (a read-merge), so a partial update never zeroes the rest. An unknown id is 404.
func (s *Server) handleUpdateDomain(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	cur, ok, err := s.dir.GetDomain(id)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	var req struct {
		Status    *int    `json:"status"`
		MaxUser   *int64  `json:"maxUser"`
		Title     *string `json:"title"`
		Address   *string `json:"address"`
		AdminName *string `json:"adminName"`
		Tel       *string `json:"tel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	upd := directory.DomainUpdate{
		Status: cur.Status, MaxUser: cur.MaxUser, Title: cur.Title,
		Address: cur.Address, AdminName: cur.AdminName, Tel: cur.Tel,
	}
	if req.Status != nil {
		upd.Status = *req.Status
	}
	if req.MaxUser != nil {
		upd.MaxUser = *req.MaxUser
	}
	if req.Title != nil {
		upd.Title = *req.Title
	}
	if req.Address != nil {
		upd.Address = *req.Address
	}
	if req.AdminName != nil {
		upd.AdminName = *req.AdminName
	}
	if req.Tel != nil {
		upd.Tel = *req.Tel
	}
	if _, err := s.dir.UpdateDomain(id, upd); err != nil {
		http.Error(w, "could not update domain: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCreateDomain provisions a domain (system administrators only); its
// homedir is derived from the configured data root.
func (s *Server) handleCreateDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "a domain name is required", http.StatusBadRequest)
		return
	}
	id, err := s.dir.CreateDomain(req.Name, s.paths.HomedirFor(req.Name))
	if err != nil {
		http.Error(w, "could not create domain: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": id, "name": req.Name})
}

// handleDeleteDomain purges a domain and everything scoped to it (its
// requirePurge wrapper gates on the DomainPurge capability). The on-disk
// mailboxes and domain directory are removed only when ?deleteFiles=true. An
// unknown domain id is 404.
func (s *Server) handleDeleteDomain(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	ok, err := s.dir.PurgeDomain(id, r.URL.Query().Get("deleteFiles") == "true")
	if err != nil {
		http.Error(w, "could not purge domain: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such domain", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package admin

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"hermex/internal/objectstore"
)

// handleGetUserQuota returns a user's store quota limits and current usage
// (system administrators only).
func (s *Server) handleGetUserQuota(w http.ResponseWriter, r *http.Request) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	limits, used, err := s.store.GetQuota(u.Maildir)
	if err != nil {
		http.Error(w, "could not read quota", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"sendKB":    limits.SendKB,
		"receiveKB": limits.ReceiveKB,
		"storageKB": limits.StorageKB,
		"usedBytes": used,
	})
}

// handleSetUserQuota replaces a user's store quota limits from a JSON body
// (system administrators only): {"sendKB":...,"receiveKB":...,"storageKB":...}.
func (s *Server) handleSetUserQuota(w http.ResponseWriter, r *http.Request) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	var q objectstore.QuotaLimits
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.store.SetQuota(u.Maildir, q); err != nil {
		http.Error(w, "could not save quota", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// quotaView is the quota section's template model: the limits shown in mebibytes
// (0 = unlimited) and the current usage, for the user detail page.
type quotaView struct {
	SendMB    uint32
	ReceiveMB uint32
	StorageMB uint32
	UsedMB    int64
}

// quotaViewOf builds the template model, converting the stored KiB limits and the
// byte usage to MiB for display.
func quotaViewOf(limits objectstore.QuotaLimits, usedBytes int64) quotaView {
	return quotaView{
		SendMB:    limits.SendKB / 1024,
		ReceiveMB: limits.ReceiveKB / 1024,
		StorageMB: limits.StorageKB / 1024,
		UsedMB:    usedBytes / (1024 * 1024),
	}
}

// quotaFromForm reads the quota form (values in MiB) into the canonical KiB
// limits. The MiB→KiB conversion is done in 64-bit and clamped to the uint32 KiB
// ceiling (~4 TiB), so a large entry cannot overflow.
func quotaFromForm(r *http.Request) objectstore.QuotaLimits {
	mbToKB := func(field string) uint32 {
		n, _ := strconv.ParseUint(r.PostFormValue(field), 10, 64)
		return uint32(min(n*1024, math.MaxUint32))
	}
	return objectstore.QuotaLimits{
		SendKB:    mbToKB("sendmb"),
		ReceiveKB: mbToKB("receivemb"),
		StorageKB: mbToKB("storagemb"),
	}
}

// handleUIUserQuota saves the user's quota limits from the detail form and returns
// the refreshed status panel; a store error is reported in the panel rather than
// failing the request.
func (s *Server) handleUIUserQuota(w http.ResponseWriter, r *http.Request) {
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
		if err := s.store.SetQuota(u.Maildir, quotaFromForm(r)); err != nil {
			data["Error"] = "Could not save quota: " + err.Error()
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, "user-status", data)
}

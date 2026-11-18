package admin

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/objectstore"
)

// handleGetUserQuota returns a user's store quota limits and current usage
// (system administrators only).
func (s *Server) handleGetUserQuota(w http.ResponseWriter, r *http.Request) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	limits, used, err := s.store.GetQuota(u.Maildir)
	if err != nil {
		s.fail(w, "could not read quota", err, http.StatusInternalServerError)
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
		s.fail(w, "server error", err, http.StatusInternalServerError)
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
		s.fail(w, "could not save quota", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// quotaView is the quota section's template model: the limits shown in mebibytes
// (0 = unlimited) and the current usage as a size message, for the user detail
// page.
type quotaView struct {
	SendMB    uint32
	ReceiveMB uint32
	StorageMB uint32
	Used      string
}

// quotaViewOf builds the template model, converting the stored KiB limits to MiB
// for the form and the byte usage to the largest fitting unit in lang.
func quotaViewOf(limits objectstore.QuotaLimits, usedBytes int64, lang string) quotaView {
	return quotaView{
		SendMB:    limits.SendKB / 1024,
		ReceiveMB: limits.ReceiveKB / 1024,
		StorageMB: limits.StorageKB / 1024,
		Used:      sizeMsg(usedBytes, lang),
	}
}

// quotaFromForm reads the quota form (values in MiB) into the canonical KiB
// limits. The MiB count is clamped to the uint32 KiB ceiling (~4 TiB) before it is
// scaled, so a large entry cannot wrap. An empty field is 0 (unlimited); anything
// else that is not a whole number is refused (ok false), never read as unlimited.
func quotaFromForm(r *http.Request) (objectstore.QuotaLimits, bool) {
	ok := true
	mbToKB := func(field string) uint32 {
		v := strings.TrimSpace(r.PostFormValue(field))
		if v == "" {
			return 0
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			ok = false
			return 0
		}
		return uint32(min(n, math.MaxUint32/1024) * 1024)
	}
	q := objectstore.QuotaLimits{
		SendKB:    mbToKB("sendmb"),
		ReceiveKB: mbToKB("receivemb"),
		StorageKB: mbToKB("storagemb"),
	}
	return q, ok
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
		data["Error"] = s.notice(userUnread, err)
	case !ok:
		data["Error"] = "userDetail.noSuchUser"
	default:
		q, valid := quotaFromForm(r)
		if !valid {
			data["Error"] = "userDetail.quotaInvalid"
		} else if err := s.store.SetQuota(u.Maildir, q); err != nil {
			data["Error"] = s.notice("userDetail.saveQuotaFailed", err)
		} else {
			data["Saved"] = true
		}
	}
	s.render(w, r, "user-status", data)
}

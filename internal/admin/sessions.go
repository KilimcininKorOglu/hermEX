package admin

import (
	"net/http"
	"time"
)

// sessionView is one live ActiveSync session's row in the mobile-devices monitor:
// the stored telemetry plus a derived status and a relative age.
type sessionView struct {
	User       string
	IP         string
	DeviceType string
	DeviceID   string
	Command    string
	ASVersion  string
	Push       bool
	AgeSec     int64
	Status     string
}

// sessionViews reads the non-stale live sessions and projects them for display,
// deriving "Active"/"Ended" and the seconds since the last activity.
func (s *Server) sessionViews() []sessionView {
	now := time.Now().Unix()
	recs, _ := s.dir.ListActiveSessions(now)
	out := make([]sessionView, 0, len(recs))
	for _, rec := range recs {
		status := "Active"
		if rec.EndedAt > 0 {
			status = "Ended"
		}
		age := max(now-rec.LastUpdate, 0)
		out = append(out, sessionView{
			User: rec.Username, IP: rec.IP, DeviceType: rec.DeviceType, DeviceID: rec.DeviceID,
			Command: rec.Command, ASVersion: rec.ASVersion, Push: rec.Push, AgeSec: age, Status: status,
		})
	}
	return out
}

// handleUIMobileDevices renders the live ActiveSync session monitor (system admins).
func (s *Server) handleUIMobileDevices(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "mobile_devices.html", map[string]any{
		"Nav": "mobiledevices", "CSRF": csrfCookieValue(r), "Sessions": s.sessionViews(),
	})
}

// handleUIMobileDevicesPanel renders just the session table for the auto-refresh poll.
func (s *Server) handleUIMobileDevicesPanel(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "sessions-panel", map[string]any{"Sessions": s.sessionViews()})
}

// handleGetMobileDevices returns the live sessions as JSON (system admins).
func (s *Server) handleGetMobileDevices(w http.ResponseWriter, r *http.Request) {
	recs, err := s.dir.ListActiveSessions(time.Now().Unix())
	if err != nil {
		http.Error(w, "could not read sessions", http.StatusInternalServerError)
		return
	}
	writeJSON(w, recs)
}

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
func (s *Server) sessionViews() ([]sessionView, error) {
	now := time.Now().Unix()
	recs, err := s.dir.ListActiveSessions(now)
	if err != nil {
		return nil, err
	}
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
	return out, nil
}

// sessionsPanelData is the session table's model. A failed read is reported
// rather than rendered as an empty table, which reads as no device connected.
func (s *Server) sessionsPanelData() map[string]any {
	views, err := s.sessionViews()
	if err != nil {
		return map[string]any{"Error": s.notice("Could not read the sessions.", err)}
	}
	return map[string]any{"Sessions": views}
}

// handleUIMobileDevices renders the live ActiveSync session monitor (system admins).
func (s *Server) handleUIMobileDevices(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	data := s.sessionsPanelData()
	data["Nav"], data["CSRF"] = "mobiledevices", csrfCookieValue(r)
	s.render(w, "mobile_devices.html", data)
}

// handleUIMobileDevicesPanel renders just the session table for the auto-refresh poll.
func (s *Server) handleUIMobileDevicesPanel(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "sessions-panel", s.sessionsPanelData())
}

// handleGetMobileDevices returns the live sessions as JSON (system admins).
func (s *Server) handleGetMobileDevices(w http.ResponseWriter, r *http.Request) {
	recs, err := s.dir.ListActiveSessions(time.Now().Unix())
	if err != nil {
		s.fail(w, "could not read sessions", err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, recs)
}

package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"hermex/internal/activesync"
)

// handleGetUserDevices returns a user's ActiveSync devices (system administrators
// only), read from the mailbox object store at the user's maildir.
func (s *Server) handleGetUserDevices(w http.ResponseWriter, r *http.Request) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	devs, err := s.store.ListDevices(u.Maildir)
	if err != nil {
		http.Error(w, "could not read devices", http.StatusInternalServerError)
		return
	}
	writeJSON(w, devs)
}

// applyDeviceAction performs a per-device management action on the mailbox at
// maildir. An unknown action is an error.
func (s *Server) applyDeviceAction(maildir, deviceID, action string) error {
	switch action {
	case "resync":
		return s.store.ResyncDevice(maildir, deviceID)
	case "delete":
		return s.store.DeleteDevice(maildir, deviceID)
	case "wipe":
		return s.store.WipeDevice(maildir, deviceID, false)
	case "wipe-account":
		return s.store.WipeDevice(maildir, deviceID, true)
	case "cancel":
		return s.store.CancelDeviceWipe(maildir, deviceID)
	default:
		return fmt.Errorf("unknown device action %q", action)
	}
}

// handleUserDeviceAction performs a per-device action from a JSON body (system
// administrators only): {"deviceId":"...","action":"resync|delete|wipe|wipe-account|cancel"}.
func (s *Server) handleUserDeviceAction(w http.ResponseWriter, r *http.Request) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	var req struct {
		DeviceID string `json:"deviceId"`
		Action   string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DeviceID == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.applyDeviceAction(u.Maildir, req.DeviceID, req.Action); err != nil {
		http.Error(w, "could not apply device action: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deviceView is one device's row in the mobile-devices table: the merged device
// info with times formatted for display, a status label, and the actions the
// current wipe state permits.
type deviceView struct {
	DeviceID      string
	DeviceUser    string
	DeviceType    string
	UserAgent     string
	ASVersion     string
	FirstSync     string
	LastSync      string
	FoldersSynced int
	Status        string
	CanWipe       bool // no wipe outstanding -> a wipe can be queued
	CanCancel     bool // a wipe is queued but not yet acknowledged -> it can be cancelled
}

// deviceTimeLayout is the read-only display form for device timestamps (local
// wall-clock); the open-ended value (0) renders empty.
const deviceTimeLayout = "2006-01-02 15:04"

func formatDeviceTime(sec int64) string {
	if sec == 0 {
		return ""
	}
	return time.Unix(sec, 0).Local().Format(deviceTimeLayout)
}

// wipeStatusLabel renders a device's remote-wipe status for display.
func wipeStatusLabel(status int) string {
	switch status {
	case activesync.WipeStatusOK:
		return "OK"
	case activesync.WipeStatusPending:
		return "Wipe pending"
	case activesync.WipeStatusRequested:
		return "Wipe requested"
	case activesync.WipeStatusWiped:
		return "Wiped"
	case activesync.WipeStatusAccountPending:
		return "Account wipe pending"
	case activesync.WipeStatusAccountRequested:
		return "Account wipe requested"
	case activesync.WipeStatusAccountWiped:
		return "Account wiped"
	default:
		return "Unknown"
	}
}

// deviceViewsOf builds the table model from the merged device list.
func deviceViewsOf(devs []activesync.DeviceInfo) []deviceView {
	out := make([]deviceView, 0, len(devs))
	for _, d := range devs {
		wiped := d.WipeStatus == activesync.WipeStatusWiped || d.WipeStatus == activesync.WipeStatusAccountWiped
		out = append(out, deviceView{
			DeviceID:      d.DeviceID,
			DeviceUser:    d.DeviceUser,
			DeviceType:    d.DeviceType,
			UserAgent:     d.UserAgent,
			ASVersion:     d.ASVersion,
			FirstSync:     formatDeviceTime(d.FirstSync),
			LastSync:      formatDeviceTime(d.LastSync),
			FoldersSynced: d.FoldersSynced,
			Status:        wipeStatusLabel(d.WipeStatus),
			CanWipe:       d.WipeStatus < activesync.WipeStatusPending,
			CanCancel:     d.WipeStatus >= activesync.WipeStatusPending && !wiped,
		})
	}
	return out
}

// handleUIUserDevices performs a per-device action from the detail page and
// returns the refreshed mobile-devices panel; an error is shown in the panel
// rather than failing the request.
func (s *Server) handleUIUserDevices(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil || !ok {
		s.renderUserDevices(w, r.PathValue("email"), csrfCookieValue(r), nil, "No such user.")
		return
	}
	errMsg := ""
	if deviceID := r.PostFormValue("deviceID"); deviceID == "" {
		errMsg = "No device specified."
	} else if err := s.applyDeviceAction(u.Maildir, deviceID, r.PostFormValue("action")); err != nil {
		errMsg = "Could not apply device action: " + err.Error()
	}
	devs, derr := s.store.ListDevices(u.Maildir)
	if derr != nil && errMsg == "" {
		errMsg = "Could not read devices: " + derr.Error()
	}
	s.renderUserDevices(w, u.Username, csrfCookieValue(r), devs, errMsg)
}

// renderUserDevices renders the mobile-devices panel for htmx after a device
// action, carrying an optional error message.
func (s *Server) renderUserDevices(w http.ResponseWriter, email, csrf string, devs []activesync.DeviceInfo, errMsg string) {
	s.render(w, "user-devices", map[string]any{
		"Email":   email,
		"CSRF":    csrf,
		"Devices": deviceViewsOf(devs),
		"Error":   errMsg,
	})
}

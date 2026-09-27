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
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	devs, err := s.store.ListDevices(u.Maildir)
	if err != nil {
		s.fail(w, "could not read devices", err, http.StatusInternalServerError)
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
		s.fail(w, "server error", err, http.StatusInternalServerError)
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
		s.fail(w, "could not apply device action", err, http.StatusBadRequest)
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
		return "userDevices.statusOK"
	case activesync.WipeStatusPending:
		return "userDevices.statusWipePending"
	case activesync.WipeStatusRequested:
		return "userDevices.statusWipeRequested"
	case activesync.WipeStatusWiped:
		return "userDevices.statusWiped"
	case activesync.WipeStatusAccountPending:
		return "userDevices.statusAccountPending"
	case activesync.WipeStatusAccountRequested:
		return "userDevices.statusAccountRequested"
	case activesync.WipeStatusAccountWiped:
		return "userDevices.statusAccountWiped"
	default:
		return "userDevices.statusUnknown"
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
	switch {
	case err != nil:
		s.renderUserDevices(w, r, r.PathValue("email"), csrfCookieValue(r), nil,
			s.notice("userDetail.userUnread", err), "userDetail.devicesUnread")

		return
	case !ok:
		s.renderUserDevices(w, r, r.PathValue("email"), csrfCookieValue(r), nil, "userDetail.noSuchUser", "")
		return
	}
	errMsg := ""
	if deviceID := r.PostFormValue("deviceID"); deviceID == "" {
		errMsg = "userDevices.noDevice"
	} else if err := s.applyDeviceAction(u.Maildir, deviceID, r.PostFormValue("action")); err != nil {
		errMsg = s.notice("userDevices.actionFailed", err)
	}
	listErr := ""
	devs, err := s.store.ListDevices(u.Maildir)
	if err != nil {
		listErr = s.notice("userDetail.devicesUnread", err)
	}
	s.renderUserDevices(w, r, u.Username, csrfCookieValue(r), devs, errMsg, listErr)
}

// renderUserDevices renders the mobile-devices panel for htmx after a device
// action, carrying an optional error message. listErr reports a failed read of the
// devices, which the table must not show as none.
func (s *Server) renderUserDevices(w http.ResponseWriter, r *http.Request, email, csrf string, devs []activesync.DeviceInfo, errMsg, listErr string) {
	s.render(w, r, "user-devices", map[string]any{
		"Email":        email,
		"CSRF":         csrf,
		"Devices":      deviceViewsOf(devs),
		"Error":        errMsg,
		"DevicesError": listErr,
	})

}

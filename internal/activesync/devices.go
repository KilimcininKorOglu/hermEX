package activesync

import (
	"encoding/json"
	"slices"
	"strings"

	"hermex/internal/objectstore"
)

// ActiveSync device-status codes, mirroring the remote-wipe lifecycle a
// management console drives. A freshly-seen device is OK; an administrator can
// request a full or account-only wipe (pending), the server delivers it on a
// Provision exchange (requested), the device acknowledges it (wiped), or the
// administrator cancels it before the device picks it up (back to OK). The
// numeric values match the wire/console status enumeration.
const (
	WipeStatusUnknown          = 0  // no status recorded
	WipeStatusOK               = 1  // active, no wipe outstanding
	WipeStatusPending          = 2  // full wipe requested, not yet delivered
	WipeStatusRequested        = 4  // full wipe delivered, awaiting acknowledgement
	WipeStatusWiped            = 8  // device acknowledged the full wipe
	WipeStatusAccountPending   = 16 // account-only wipe requested, not yet delivered
	WipeStatusAccountRequested = 32 // account-only wipe delivered, awaiting acknowledgement
	WipeStatusAccountWiped     = 64 // device acknowledged the account-only wipe
)

// Provision wipe-emit selector: which remote-wipe element a Provision response
// must carry for a device, if any.
const (
	wipeEmitNone    = iota // no wipe outstanding
	wipeEmitFull           // a <RemoteWipe/> element (full device reset)
	wipeEmitAccount        // an <AccountOnlyRemoteWipe/> element
)

// wipeOutstanding reports whether a remote wipe is pending delivery or
// acknowledgement for a device — anything at or past the pending threshold.
func wipeOutstanding(status int) bool {
	return status >= WipeStatusPending
}

// deviceMeta is one device's ActiveSync metadata, recorded best-effort on each
// command so the management console can show what last connected. It is stored
// apart from the sync-state blob (see PrActiveSyncDevices) so a metadata write
// never collides with a concurrent sync-key update.
type deviceMeta struct {
	DeviceUser string `json:"deviceUser,omitempty"`
	DeviceType string `json:"deviceType,omitempty"`
	UserAgent  string `json:"userAgent,omitempty"`
	ASVersion  string `json:"asVersion,omitempty"`
	FirstSync  int64  `json:"firstSync,omitempty"`
	LastSync   int64  `json:"lastSync,omitempty"`
	WipeStatus int    `json:"wipeStatus,omitempty"`
}

// devicesMeta is the whole mailbox's ActiveSync device metadata, persisted as
// JSON in the store-root PrActiveSyncDevices property, keyed by device id.
type devicesMeta struct {
	Devices map[string]*deviceMeta `json:"devices,omitempty"`
}

// device returns the device's metadata, creating it if absent.
func (m *devicesMeta) device(id string) *deviceMeta {
	if m.Devices == nil {
		m.Devices = map[string]*deviceMeta{}
	}
	d := m.Devices[id]
	if d == nil {
		d = &deviceMeta{}
		m.Devices[id] = d
	}
	return d
}

// loadDevices reads the mailbox's ActiveSync device metadata, returning an empty
// set when no device has been recorded yet.
func loadDevices(st *objectstore.Store) (*devicesMeta, error) {
	raw, err := st.GetActiveSyncDevices()
	if err != nil {
		return nil, err
	}
	m := &devicesMeta{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), m); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// saveDevices persists the mailbox's ActiveSync device metadata.
func saveDevices(st *objectstore.Store, m *devicesMeta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return st.SetActiveSyncDevices(string(b))
}

// recordDeviceContact stamps a device's metadata on the store and returns the
// device's current wipe status. The caller treats the write as best-effort — it
// is a pure side effect that must never alter or fail a command response.
// firstSync and the OK status are set once; lastSync and the live attributes
// refresh every call. A blank device id is a no-op.
func recordDeviceContact(st *objectstore.Store, deviceID, user, deviceType, userAgent, asVersion string, now int64) (int, error) {
	if deviceID == "" {
		return WipeStatusUnknown, nil
	}
	m, err := loadDevices(st)
	if err != nil {
		return WipeStatusUnknown, err
	}
	d := m.device(deviceID)
	if d.FirstSync == 0 {
		d.FirstSync = now
	}
	// Initialize an unseen device to OK, but never overwrite a wipe an
	// administrator may have queued before the device's first contact landed.
	if d.WipeStatus == WipeStatusUnknown {
		d.WipeStatus = WipeStatusOK
	}
	d.LastSync = now
	d.DeviceUser = user
	if deviceType != "" {
		d.DeviceType = deviceType
	}
	if userAgent != "" {
		d.UserAgent = userAgent
	}
	if asVersion != "" {
		d.ASVersion = asVersion
	}
	if err := saveDevices(st, m); err != nil {
		return WipeStatusUnknown, err
	}
	return d.WipeStatus, nil
}

// advanceProvisionWipe reads a device's outstanding remote-wipe state, advances
// it for one Provision exchange, and reports which wipe element the response must
// carry (wipeEmitNone/Full/Account). The device's acknowledgement (acked) drives
// the transition into the wiped state; otherwise the wipe moves to "requested"
// and is re-sent until the device acknowledges. A device with no outstanding wipe
// yields wipeEmitNone and is left untouched.
func advanceProvisionWipe(st *objectstore.Store, deviceID string, acked bool) (int, error) {
	m, err := loadDevices(st)
	if err != nil {
		return wipeEmitNone, err
	}
	d := m.Devices[deviceID]
	if d == nil || !wipeOutstanding(d.WipeStatus) {
		return wipeEmitNone, nil
	}
	var emit int
	if d.WipeStatus <= WipeStatusWiped {
		emit = wipeEmitFull
		if acked {
			d.WipeStatus = WipeStatusWiped
		} else {
			d.WipeStatus = WipeStatusRequested
		}
	} else {
		emit = wipeEmitAccount
		if acked {
			d.WipeStatus = WipeStatusAccountWiped
		} else {
			d.WipeStatus = WipeStatusAccountRequested
		}
	}
	if err := saveDevices(st, m); err != nil {
		return wipeEmitNone, err
	}
	return emit, nil
}

// ResyncDevice clears a device's sync state so it re-primes its folder hierarchy
// and collections on the next sync, while leaving the device recorded (it stays
// in the device list with its metadata).
func ResyncDevice(st *objectstore.Store, deviceID string) error {
	state, err := loadState(st)
	if err != nil {
		return err
	}
	if _, ok := state.Devices[deviceID]; !ok {
		return nil
	}
	delete(state.Devices, deviceID)
	return saveState(st, state)
}

// DeleteDevice removes a device entirely — both its sync state and its recorded
// metadata — so it disappears from the device list until it next connects.
func DeleteDevice(st *objectstore.Store, deviceID string) error {
	state, err := loadState(st)
	if err != nil {
		return err
	}
	if _, ok := state.Devices[deviceID]; ok {
		delete(state.Devices, deviceID)
		if err := saveState(st, state); err != nil {
			return err
		}
	}
	meta, err := loadDevices(st)
	if err != nil {
		return err
	}
	if _, ok := meta.Devices[deviceID]; ok {
		delete(meta.Devices, deviceID)
		return saveDevices(st, meta)
	}
	return nil
}

// RequestWipe queues a remote wipe for a device: a full device reset, or an
// account-only wipe when accountOnly is set. The wipe is delivered on the
// device's next Provision exchange (forced by HTTP 449 on other commands).
func RequestWipe(st *objectstore.Store, deviceID string, accountOnly bool) error {
	status := WipeStatusPending
	if accountOnly {
		status = WipeStatusAccountPending
	}
	return setDeviceWipeStatus(st, deviceID, status)
}

// CancelWipe clears a queued remote wipe for a device, returning it to OK. It has
// no effect once the device has acknowledged the wipe.
func CancelWipe(st *objectstore.Store, deviceID string) error {
	return setDeviceWipeStatus(st, deviceID, WipeStatusOK)
}

// setDeviceWipeStatus sets a recorded device's wipe status, creating the metadata
// record if the administrator acts before the device's first contact lands.
func setDeviceWipeStatus(st *objectstore.Store, deviceID string, status int) error {
	meta, err := loadDevices(st)
	if err != nil {
		return err
	}
	meta.device(deviceID).WipeStatus = status
	return saveDevices(st, meta)
}

// DeviceInfo is the read-only view of one ActiveSync device for the management
// console: the recorded metadata merged with the live count of synced
// collections from the sync state.
type DeviceInfo struct {
	DeviceID      string `json:"deviceId"`
	DeviceUser    string `json:"deviceUser"`
	DeviceType    string `json:"deviceType"`
	UserAgent     string `json:"userAgent"`
	ASVersion     string `json:"asVersion"`
	FirstSync     int64  `json:"firstSync"`
	LastSync      int64  `json:"lastSync"`
	FoldersSynced int    `json:"foldersSynced"`
	WipeStatus    int    `json:"wipeStatus"`
}

// Devices returns the mailbox's ActiveSync devices, sorted by id, merging the
// recorded metadata with the live synced-folder count from the sync state. A
// device present in either source appears, so the console never hides a device
// that has sync state but no recorded metadata or vice versa.
func Devices(st *objectstore.Store) ([]DeviceInfo, error) {
	meta, err := loadDevices(st)
	if err != nil {
		return nil, err
	}
	state, err := loadState(st)
	if err != nil {
		return nil, err
	}
	ids := map[string]struct{}{}
	for id := range meta.Devices {
		ids[id] = struct{}{}
	}
	for id := range state.Devices {
		ids[id] = struct{}{}
	}
	out := make([]DeviceInfo, 0, len(ids))
	for id := range ids {
		info := DeviceInfo{DeviceID: id}
		if d := meta.Devices[id]; d != nil {
			info.DeviceUser = d.DeviceUser
			info.DeviceType = d.DeviceType
			info.UserAgent = d.UserAgent
			info.ASVersion = d.ASVersion
			info.FirstSync = d.FirstSync
			info.LastSync = d.LastSync
			info.WipeStatus = d.WipeStatus
		}
		if d := state.Devices[id]; d != nil {
			info.FoldersSynced = len(d.Collections)
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b DeviceInfo) int { return strings.Compare(a.DeviceID, b.DeviceID) })
	return out, nil
}

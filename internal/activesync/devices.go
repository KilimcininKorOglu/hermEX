package activesync

import (
	"encoding/json"
	"slices"
	"strings"

	"hermex/internal/objectstore"
)

// ActiveSync device-status codes, mirroring the remote-wipe lifecycle a
// management console drives. A freshly-seen device is OK; an administrator can
// request a full or account-only wipe (pending), the device acknowledges it
// (wiped), or the administrator cancels it before the device picks it up (back
// to OK). The numeric values match the wire/console status enumeration.
const (
	WipeStatusUnknown     = 0  // no status recorded
	WipeStatusOK          = 1  // active, no wipe outstanding
	WipeStatusPending     = 2  // full wipe requested, not yet delivered
	WipeStatusRequested   = 4  // wipe delivered, awaiting device acknowledgement
	WipeStatusWiped       = 8  // device acknowledged the wipe
	WipeStatusAccountWipe = 16 // account-only wipe pending
)

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

// recordDeviceContact stamps a device's metadata on the store. The caller treats
// it as best-effort — it is a pure side effect that must never alter or fail a
// command response. firstSync and the OK status are set once; lastSync and the
// live attributes refresh every call. A blank device id is a no-op.
func recordDeviceContact(st *objectstore.Store, deviceID, user, deviceType, userAgent, asVersion string, now int64) error {
	if deviceID == "" {
		return nil
	}
	m, err := loadDevices(st)
	if err != nil {
		return err
	}
	d := m.device(deviceID)
	if d.FirstSync == 0 {
		d.FirstSync = now
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
	return saveDevices(st, m)
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

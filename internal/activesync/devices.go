package activesync

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"slices"
	"strconv"
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

// wipeOutstanding reports whether a remote wipe is pending delivery or awaiting
// the device's acknowledgement, the states that force re-provisioning and re-emit
// the directive. The terminal wiped states are NOT outstanding: a device that
// completed a wipe (and, after an account-only wipe, may re-add the account on the
// same hardware) must not be wiped again in a loop.
func wipeOutstanding(status int) bool {
	switch status {
	case WipeStatusPending, WipeStatusRequested, WipeStatusAccountPending, WipeStatusAccountRequested:
		return true
	}
	return false
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
	// PendingKey is the temporary policy key phase one of Provision handed the
	// device and PendingFor the policy key it was issued under; AckedKey is the
	// final key the device earned by acknowledging that policy. [MS-ASPROV] lets
	// only the acknowledgement earn the final key, so the command gate reads
	// AckedKey and never trusts the key a request merely carries.
	PendingKey string `json:"pendingKey,omitempty"`
	PendingFor string `json:"pendingFor,omitempty"`
	AckedKey   string `json:"ackedKey,omitempty"`
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

// updateDevices applies change to the mailbox's device metadata as stored at the
// moment of the write, inside one store transaction, like updateState: the
// daemon records every device's contact while the admin panel queues wipes from
// another process, so a writer must never put back a copy it read earlier.
// change reports whether it changed anything and must not use the store.
func updateDevices(st *objectstore.Store, change func(*devicesMeta) bool) error {
	return st.UpdateActiveSyncDevices(func(raw string) (string, bool, error) {
		m := &devicesMeta{}
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), m); err != nil {
				return "", false, err
			}
		}
		if !change(m) {
			return "", false, nil
		}
		b, err := json.Marshal(m)
		return string(b), err == nil, err
	})
}

// recordDeviceContact stamps a device's metadata on the store and returns a copy
// of the device's record as written, which carries its wipe status and the policy
// key it acknowledged. firstSync and the OK status are set once; lastSync and the
// live attributes refresh every call. A blank device id is a no-op.
func recordDeviceContact(st *objectstore.Store, deviceID, user, deviceType, userAgent, asVersion string, now int64) (deviceMeta, error) {
	if deviceID == "" {
		return deviceMeta{}, nil
	}
	var snap deviceMeta
	err := updateDevices(st, func(m *devicesMeta) bool {
		d := m.device(deviceID)
		d.stampContact(user, deviceType, userAgent, asVersion, now)
		snap = *d
		return true
	})
	if err != nil {
		return deviceMeta{}, err
	}
	return snap, nil
}

// issuePendingKey stores a fresh temporary policy key for a device's Provision
// phase one, under the policy key final, and returns it.
func issuePendingKey(st *objectstore.Store, deviceID, final string) (string, error) {
	temp, err := tempPolicyKey(final)
	if err != nil {
		return "", err
	}
	err = updateDevices(st, func(m *devicesMeta) bool {
		d := m.device(deviceID)
		d.PendingKey, d.PendingFor = temp, final
		return true
	})
	if err != nil {
		return "", err
	}
	return temp, nil
}

// tempPolicyKey draws a random 32-bit policy key, the width the packed command
// line carries. It is never 0 (the not-provisioned key), 1 (the key of a mailbox
// with no policy) or final, so it cannot be taken for any of them.
func tempPolicyKey(final string) (string, error) {
	var b [4]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		k := strconv.FormatUint(uint64(binary.LittleEndian.Uint32(b[:])), 10)
		if k != "0" && k != "1" && k != final {
			return k, nil
		}
	}
}

// acknowledgePendingKey consumes a device's temporary policy key. When key is the
// one phase one issued, under the policy that is still current (final), the device
// earns final and the temporary key is cleared, so it cannot be acknowledged a
// second time. It reports whether the acknowledgement matched.
func acknowledgePendingKey(st *objectstore.Store, deviceID, key, final string) (bool, error) {
	matched := false
	err := updateDevices(st, func(m *devicesMeta) bool {
		d := m.Devices[deviceID]
		if d == nil || d.PendingKey == "" || d.PendingKey != key || d.PendingFor != final {
			return false
		}
		d.PendingKey, d.PendingFor, d.AckedKey = "", "", final
		matched = true
		return true
	})
	if err != nil {
		return false, err
	}
	return matched, nil
}

// stampContact records one contact on a device's metadata: firstSync and the OK
// status once, lastSync and the live attributes every time.
func (d *deviceMeta) stampContact(user, deviceType, userAgent, asVersion string, now int64) {
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
}

// advanceProvisionWipe reads a device's outstanding remote-wipe state, advances
// it for one Provision exchange, and reports which wipe element the response must
// carry (wipeEmitNone/Full/Account). The device's acknowledgement (acked) drives
// the transition into the wiped state; otherwise the wipe moves to "requested"
// and is re-sent until the device acknowledges. A device with no outstanding wipe
// yields wipeEmitNone and is left untouched.
func advanceProvisionWipe(st *objectstore.Store, deviceID string, acked bool) (int, error) {
	emit := wipeEmitNone
	err := updateDevices(st, func(m *devicesMeta) bool {
		d := m.Devices[deviceID]
		if d == nil || !wipeOutstanding(d.WipeStatus) {
			return false
		}
		emit = d.advanceWipe(acked)
		return true
	})
	if err != nil {
		return wipeEmitNone, err
	}
	return emit, nil
}

// advanceWipe moves an outstanding wipe one Provision exchange forward and
// reports which wipe element the response carries.
func (d *deviceMeta) advanceWipe(acked bool) int {
	if d.WipeStatus <= WipeStatusWiped {
		d.WipeStatus = WipeStatusRequested
		if acked {
			d.WipeStatus = WipeStatusWiped
		}
		return wipeEmitFull
	}
	d.WipeStatus = WipeStatusAccountRequested
	if acked {
		d.WipeStatus = WipeStatusAccountWiped
	}
	return wipeEmitAccount
}

// ResyncDevice clears a device's sync state so it re-primes its folder hierarchy
// and collections on the next sync, while leaving the device recorded (it stays
// in the device list with its metadata).
func ResyncDevice(st *objectstore.Store, deviceID string) error {
	return dropDeviceState(st, deviceID)
}

// dropDeviceState removes one device's sync state, leaving the other devices'.
func dropDeviceState(st *objectstore.Store, deviceID string) error {
	return updateState(st, func(s *asState) bool {
		if _, ok := s.Devices[deviceID]; !ok {
			return false
		}
		delete(s.Devices, deviceID)
		return true
	})
}

// DeleteDevice removes a device entirely, both its sync state and its recorded
// metadata, so it disappears from the device list until it next connects.
func DeleteDevice(st *objectstore.Store, deviceID string) error {
	if err := dropDeviceState(st, deviceID); err != nil {
		return err
	}
	return updateDevices(st, func(m *devicesMeta) bool {
		if _, ok := m.Devices[deviceID]; !ok {
			return false
		}
		delete(m.Devices, deviceID)
		return true
	})
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
	return updateDevices(st, func(m *devicesMeta) bool {
		d := m.Devices[deviceID]
		if d == nil || d.WipeStatus == WipeStatusWiped || d.WipeStatus == WipeStatusAccountWiped {
			return false
		}
		d.WipeStatus = WipeStatusOK
		return true
	})
}

// setDeviceWipeStatus sets a recorded device's wipe status, creating the metadata
// record if the administrator acts before the device's first contact lands.
func setDeviceWipeStatus(st *objectstore.Store, deviceID string, status int) error {
	return updateDevices(st, func(m *devicesMeta) bool {
		m.device(deviceID).WipeStatus = status
		return true
	})
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

package activesync

import (
	"encoding/json"
	"strconv"

	"hermex/internal/objectstore"
)

// asState is the whole mailbox's ActiveSync state, persisted as JSON in the
// store-root PrActiveSyncState property (one blob per mailbox, keyed internally
// by device). It mirrors the webmail-settings/out-of-office store pattern rather
// than introducing a dedicated table.
type asState struct {
	Devices map[string]*deviceState `json:"devices,omitempty"`
}

// deviceState is one device's sync state: the folder-hierarchy sync key and the
// per-collection content sync state.
type deviceState struct {
	HierarchyKey string                      `json:"hierarchyKey,omitempty"`
	Collections  map[string]*collectionState `json:"collections,omitempty"`
}

// collectionState is one collection's content sync state for a device: the
// current sync key and the item snapshot (ServerId -> flag/read bits) that the
// snapshot-diff Sync compares the live folder against.
type collectionState struct {
	SyncKey string           `json:"syncKey,omitempty"`
	Items   map[string]int64 `json:"items,omitempty"`
}

// device returns the device's state, creating it if absent.
func (s *asState) device(id string) *deviceState {
	if s.Devices == nil {
		s.Devices = map[string]*deviceState{}
	}
	d := s.Devices[id]
	if d == nil {
		d = &deviceState{}
		s.Devices[id] = d
	}
	return d
}

// collection returns the device's per-collection state, creating it if absent.
func (d *deviceState) collection(id string) *collectionState {
	if d.Collections == nil {
		d.Collections = map[string]*collectionState{}
	}
	c := d.Collections[id]
	if c == nil {
		c = &collectionState{}
		d.Collections[id] = c
	}
	return c
}

// loadState reads the mailbox's ActiveSync state, returning an empty state when
// no device has synced yet.
func loadState(st *objectstore.Store) (*asState, error) {
	raw, err := st.GetActiveSyncState()
	if err != nil {
		return nil, err
	}
	s := &asState{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// saveState persists the mailbox's ActiveSync state.
func saveState(st *objectstore.Store, s *asState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.SetActiveSyncState(string(b))
}

// nextSyncKey returns the successor of an opaque integer sync key; an empty or
// unparseable key yields "1" (a fresh prime). v1 uses a plain monotonic counter,
// not the {UUID}N form — the key is opaque to the client, and a stale key after
// a re-prime is rejected as a mismatch, forcing the client to re-prime.
func nextSyncKey(key string) string {
	n, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		return "1"
	}
	return strconv.FormatUint(n+1, 10)
}

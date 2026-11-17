package activesync

import (
	"encoding/json"
	"strconv"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// asState is the whole mailbox's ActiveSync state, persisted as JSON in the
// store-root PrActiveSyncState property (one blob per mailbox, keyed internally
// by device). It mirrors the webmail-settings/out-of-office store pattern rather
// than introducing a dedicated table.
type asState struct {
	Devices map[string]*deviceState `json:"devices,omitempty"`
}

// deviceState is one device's sync state: the folder-hierarchy sync key, the
// per-collection content sync state, and the Ping heartbeat (seconds) and folder
// list the server caches for a later Ping that omits them.
type deviceState struct {
	HierarchyKey  string                      `json:"hierarchyKey,omitempty"`
	Collections   map[string]*collectionState `json:"collections,omitempty"`
	PingHeartbeat int64                       `json:"pingHeartbeat,omitempty"`
	PingFolders   []string                    `json:"pingFolders,omitempty"`
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

// updateState applies change to the mailbox's ActiveSync state as stored at the
// moment of the write, inside one store transaction. Every device of the mailbox
// shares the one stored value, and the admin panel writes it from another
// process, so a command must not write back a copy it read earlier: that would
// erase whatever another writer committed in between. change edits only the
// fields its command owns and reports whether anything changed; it must not use
// the store.
func updateState(st *objectstore.Store, change func(*asState) bool) error {
	return st.UpdateActiveSyncState(func(raw string) (string, bool, error) {
		s := &asState{}
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), s); err != nil {
				return "", false, err
			}
		}
		if !change(s) {
			return "", false, nil
		}
		b, err := json.Marshal(s)
		return string(b), err == nil, err
	})
}

// collectionKeys records the sync key each collection a Sync names holds before
// the Sync runs, "" for a collection the device has not synced.
func collectionKeys(dev *deviceState, collections *wbxml.Node) map[string]string {
	keys := map[string]string{}
	for _, c := range collections.Children {
		id := c.ChildText(wbxml.ASCollectionID)
		if c.Tag != wbxml.ASCollection || id == "" {
			continue
		}
		keys[id] = ""
		if cs := dev.Collections[id]; cs != nil {
			keys[id] = cs.SyncKey
		}
	}
	return keys
}

// commitCollections writes the collections a Sync ran, as dev now holds them,
// into the stored state. A collection whose stored key is no longer the one
// recorded in before (another request or an administrator's resync committed in
// between) is left as stored; the device's next Sync then fails the key check and
// re-primes it.
func commitCollections(st *objectstore.Store, deviceID string, dev *deviceState, before map[string]string) error {
	return updateState(st, func(s *asState) bool {
		stored := s.device(deviceID)
		changed := false
		for id, key := range before {
			cur := ""
			if cs := stored.Collections[id]; cs != nil {
				cur = cs.SyncKey
			}
			if cur != key {
				continue
			}
			stored.collection(id)
			stored.Collections[id] = dev.collection(id)
			changed = true
		}
		return changed
	})
}

// advanceHierarchy moves a device's hierarchy key from the key its command
// validated to the next one, answering the new key. ok is false when the stored
// key has changed since the command read it (a re-prime or an administrator's
// resync committed in between): nothing is written and the device must re-prime.
func advanceHierarchy(st *objectstore.Store, deviceID, validated string) (key string, ok bool, err error) {
	key = nextSyncKey(validated)
	err = updateState(st, func(s *asState) bool {
		d := s.device(deviceID)
		if d.HierarchyKey != validated {
			return false
		}
		d.HierarchyKey, ok = key, true
		return true
	})
	return key, ok, err
}

// nextSyncKey returns the successor of an opaque integer sync key; an empty or
// unparseable key yields "1" (a fresh prime). v1 uses a plain monotonic counter,
// not the {UUID}N form, the key is opaque to the client, and a stale key after
// a re-prime is rejected as a mismatch, forcing the client to re-prime.
func nextSyncKey(key string) string {
	n, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		return "1"
	}
	return strconv.FormatUint(n+1, 10)
}

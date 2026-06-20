package objectstore

import (
	"encoding/json"

	"hermex/internal/easpolicy"
	"hermex/internal/mapi"
)

// GetSyncPolicy returns the mailbox's per-user ActiveSync device-policy override, or
// nil when none is set (the mailbox inherits the global default). The override lives as
// a single store-root property (PrSyncPolicy), the same per-mailbox "everything is a
// property" shape the delegate and out-of-office settings use.
func (s *Store) GetSyncPolicy() (easpolicy.Policy, error) {
	props, err := s.GetStoreProperties(mapi.PrSyncPolicy)
	if err != nil {
		return nil, err
	}
	v, ok := props.Get(mapi.PrSyncPolicy)
	if !ok {
		return nil, nil
	}
	raw, ok := v.(string)
	if !ok || raw == "" {
		return nil, nil
	}
	var p easpolicy.Policy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	return p, nil
}

// SetSyncPolicy replaces the mailbox's per-user device-policy override. An empty policy
// clears it (a later read returns nil and the mailbox inherits the global default).
func (s *Store) SetSyncPolicy(p easpolicy.Policy) error {
	raw := ""
	if len(p) > 0 {
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrSyncPolicy, Value: raw},
	})
}

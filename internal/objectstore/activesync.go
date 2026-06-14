package objectstore

import "hermex/internal/mapi"

// GetActiveSyncState returns the ActiveSync sync-state JSON stored on the store
// root, or the empty string when no device has synced yet. The state lives as a
// single MAPI property (PrActiveSyncState) rather than in a dedicated table —
// the same "everything is a property" shape the rest of the store uses, mirroring
// the webmail-settings and out-of-office stores.
func (s *Store) GetActiveSyncState() (string, error) {
	props, err := s.GetStoreProperties(mapi.PrActiveSyncState)
	if err != nil {
		return "", err
	}
	if v, ok := props.Get(mapi.PrActiveSyncState); ok {
		if str, ok := v.(string); ok {
			return str, nil
		}
	}
	return "", nil
}

// SetActiveSyncState saves the ActiveSync sync-state JSON as the store-root
// property, replacing any previous value.
func (s *Store) SetActiveSyncState(stateJSON string) error {
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrActiveSyncState, Value: stateJSON},
	})
}

package objectstore

import "hermex/internal/mapi"

// GetEwsState returns the EWS sync-state JSON stored on the store root, or the
// empty string when no client has synced yet. The state lives as a single MAPI
// property (PrEwsSyncState) rather than in a dedicated table — the same
// "everything is a property" shape the ActiveSync, webmail-settings, and
// out-of-office stores use.
func (s *Store) GetEwsState() (string, error) {
	props, err := s.GetStoreProperties(mapi.PrEwsSyncState)
	if err != nil {
		return "", err
	}
	if v, ok := props.Get(mapi.PrEwsSyncState); ok {
		if str, ok := v.(string); ok {
			return str, nil
		}
	}
	return "", nil
}

// SetEwsState saves the EWS sync-state JSON as the store-root property,
// replacing any previous value.
func (s *Store) SetEwsState(stateJSON string) error {
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrEwsSyncState, Value: stateJSON},
	})
}

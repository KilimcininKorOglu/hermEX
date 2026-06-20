package objectstore

import (
	"encoding/json"

	"hermex/internal/mapi"
)

// GetDelegates returns the mailbox's public-delegate list — the SMTP addresses
// permitted to act for this mailbox — or nil when none have been set. The list
// lives as a single store-root property (PrAbDelegates), the same "everything is
// a property" shape the out-of-office and ActiveSync stores use, mirroring the
// reference's per-mailbox delegate storage.
func (s *Store) GetDelegates() ([]string, error) {
	props, err := s.GetStoreProperties(mapi.PrAbDelegates)
	if err != nil {
		return nil, err
	}
	v, ok := props.Get(mapi.PrAbDelegates)
	if !ok {
		return nil, nil
	}
	raw, ok := v.(string)
	if !ok || raw == "" {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	return list, nil
}

// SetDelegates replaces the mailbox's public-delegate list with the given SMTP
// addresses. An empty list clears it (a later read returns no delegates).
func (s *Store) SetDelegates(list []string) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrAbDelegates, Value: string(raw)},
	})
}

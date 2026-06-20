package objectstore

import (
	"encoding/json"
	"strings"

	"hermex/internal/mapi"
)

// GetStoreOwners returns the mailbox's additional store-owner list — the SMTP
// addresses granted read-write access to every object in the mailbox — or nil when
// none have been set. The list lives as a single store-root property
// (PrAbStoreOwners), the same "everything is a property" shape the delegate and
// send-as lists use, and deliberately not the wire-editable folder permission table so
// a client's folder-permission edit cannot drop the privileged grant.
func (s *Store) GetStoreOwners() ([]string, error) {
	props, err := s.GetStoreProperties(mapi.PrAbStoreOwners)
	if err != nil {
		return nil, err
	}
	v, ok := props.Get(mapi.PrAbStoreOwners)
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

// SetStoreOwners replaces the mailbox's additional store-owner list with the given
// SMTP addresses. An empty list clears it (a later read returns no owners).
func (s *Store) SetStoreOwners(list []string) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrAbStoreOwners, Value: string(raw)},
	})
}

// IsStoreOwner reports whether username is an additional store owner of this mailbox.
// The match is case-insensitive, the same case-folded identity the delegate-list and
// folder-permission checks use, so a grant is honored regardless of the caller's
// address casing.
func (s *Store) IsStoreOwner(username string) (bool, error) {
	owners, err := s.GetStoreOwners()
	if err != nil {
		return false, err
	}
	for _, o := range owners {
		if strings.EqualFold(o, username) {
			return true, nil
		}
	}
	return false, nil
}

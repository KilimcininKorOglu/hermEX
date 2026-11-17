package objectstore

import (
	"encoding/json"

	"hermex/internal/mapi"
)

// GetDelegates returns the mailbox's public-delegate list, the SMTP addresses
// permitted to act for this mailbox, or nil when none have been set. The list
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

// UpdateDelegates applies update to the public-delegate list inside one write
// transaction, so an edit made through another protocol (EWS AddDelegate, the
// Outlook address book, webmail) between this read and this write is not erased.
// update must not touch the store; see updateStoreString.
func (s *Store) UpdateDelegates(update func(list []string) []string) error {
	return s.updateJSONList(mapi.PrAbDelegates, update)
}

// UpdateSendAs applies update to the send-as list inside one write transaction,
// like UpdateDelegates.
func (s *Store) UpdateSendAs(update func(list []string) []string) error {
	return s.updateJSONList(mapi.PrAbSendAs, update)
}

// UpdateSendOnBehalf applies update to the send-on-behalf list inside one write
// transaction, like UpdateDelegates.
func (s *Store) UpdateSendOnBehalf(update func(list []string) []string) error {
	return s.updateJSONList(mapi.PrAbSendOnBehalf, update)
}

// updateJSONList applies update to a store-root address list held as a JSON
// array string, reading and writing it in one transaction.
func (s *Store) updateJSONList(tag mapi.PropTag, update func(list []string) []string) error {
	return s.updateStoreString(tag, func(raw string) (string, bool, error) {
		var list []string
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &list); err != nil {
				return "", false, err
			}
		}
		next, err := json.Marshal(update(list))
		if err != nil {
			return "", false, err
		}
		return string(next), true, nil
	})
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

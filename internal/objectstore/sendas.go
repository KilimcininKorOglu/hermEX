package objectstore

import (
	"encoding/json"

	"hermex/internal/mapi"
)

// GetSendAs returns the mailbox's send-as list — the SMTP addresses permitted to
// send mail as this mailbox (the From identity, distinct from the on-behalf-of
// delegate list) — or nil when none have been set. The list lives as a single
// store-root property (PrAbSendAs), the same "everything is a property" shape the
// delegate, out-of-office, and ActiveSync stores use.
func (s *Store) GetSendAs() ([]string, error) {
	props, err := s.GetStoreProperties(mapi.PrAbSendAs)
	if err != nil {
		return nil, err
	}
	v, ok := props.Get(mapi.PrAbSendAs)
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

// SetSendAs replaces the mailbox's send-as list with the given SMTP addresses. An
// empty list clears it (a later read returns no grants).
func (s *Store) SetSendAs(list []string) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrAbSendAs, Value: string(raw)},
	})
}

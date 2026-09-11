package objectstore

import (
	"encoding/json"

	"hermex/internal/mapi"
)

// GetSendOnBehalf returns the mailbox's send-on-behalf-of list, the SMTP addresses
// permitted to send mail that names this mailbox in From and the real sender in
// Sender, or nil when none have been set. It is the grant the send-as list is not:
// a send-as grant puts only this mailbox on the message, an on-behalf grant puts
// both. The list lives as a single store-root property (PrAbSendOnBehalf), the same
// shape the send-as and delegate lists use.
func (s *Store) GetSendOnBehalf() ([]string, error) {
	props, err := s.GetStoreProperties(mapi.PrAbSendOnBehalf)
	if err != nil {
		return nil, err
	}
	v, ok := props.Get(mapi.PrAbSendOnBehalf)
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

// SetSendOnBehalf replaces the mailbox's send-on-behalf-of list with the given SMTP
// addresses. An empty list clears it (a later read returns no grants).
func (s *Store) SetSendOnBehalf(list []string) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrAbSendOnBehalf, Value: string(raw)},
	})
}

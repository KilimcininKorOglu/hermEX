package objectstore

import (
	"encoding/json"

	"hermex/internal/mapi"
)

// UserConfig is one EWS user-configuration object: a named blob attached to a
// folder, carrying a typed dictionary plus opaque XML and binary sections. It is
// stored as a record in the mailbox's PrUserConfigurations JSON array, keyed by
// the resolving folder id and the configuration name. The XML and binary
// sections are kept as their base64 wire text verbatim so a Get re-emits exactly
// what a Create or Update wrote.
type UserConfig struct {
	FID     int64             `json:"fid"`
	Name    string            `json:"name"`
	Dict    []UserConfigEntry `json:"dict,omitempty"`
	XMLData string            `json:"xml,omitempty"`
	BinData string            `json:"bin,omitempty"`
}

// UserConfigEntry is one typed dictionary entry of a UserConfig. The key and the
// value each carry their own EWS dictionary-object type (String, Integer32,
// Boolean, DateTime, ByteArray, the *Array variants, etc.) and one or more string
// values; array types carry multiple values. Both the type and the value list are
// stored verbatim so the entry round-trips with its type intact.
type UserConfigEntry struct {
	KeyType   string   `json:"kt"`
	KeyValues []string `json:"kv"`
	ValType   string   `json:"vt"`
	ValValues []string `json:"vv"`
}

// userConfigStore is the on-disk shape of PrUserConfigurations.
type userConfigStore struct {
	Records []UserConfig `json:"records"`
}

// GetUserConfigs returns every stored user-configuration object for the mailbox,
// or an empty slice when none are stored.
func (s *Store) GetUserConfigs() ([]UserConfig, error) {
	props, err := s.GetStoreProperties(mapi.PrUserConfigurations)
	if err != nil {
		return nil, err
	}
	v, ok := props.Get(mapi.PrUserConfigurations)
	if !ok {
		return nil, nil
	}
	raw, ok := v.(string)
	if !ok || raw == "" {
		return nil, nil
	}
	var data userConfigStore
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, err
	}
	return data.Records, nil
}

// SetUserConfigs replaces the mailbox's stored user-configuration objects.
func (s *Store) SetUserConfigs(records []UserConfig) error {
	b, err := json.Marshal(userConfigStore{Records: records})
	if err != nil {
		return err
	}
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrUserConfigurations, Value: string(b)},
	})
}

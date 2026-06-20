package objectstore

import (
	"encoding/json"

	"hermex/internal/mapi"
)

// OOFSettings is a mailbox's out-of-office (automatic reply) configuration. An
// auto-reply fires at delivery time when OOFActive reports true for the delivery
// time. Internal and external senders may receive a different subject and body;
// external replies are sent only when ExternalEnabled, and then only to the
// audience ExternalAudience selects.
type OOFSettings struct {
	Enabled          bool   `json:"enabled"`
	InternalSubject  string `json:"internalSubject"`
	InternalReply    string `json:"internalReply"`
	ExternalSubject  string `json:"externalSubject"`
	ExternalReply    string `json:"externalReply"`
	ExternalEnabled  bool   `json:"externalEnabled"`
	ExternalAudience int    `json:"externalAudience"` // see OOFExternal* below
	Start            int64  `json:"start"`            // unix seconds; 0 = no start bound
	End              int64  `json:"end"`              // unix seconds; 0 = no end bound
}

// External-audience values for OOFSettings.ExternalAudience: who outside the
// organization receives the external auto-reply when ExternalEnabled. The zero
// value is All, so a config saved before the audience field existed keeps
// replying to every external sender (no behaviour change on upgrade).
const (
	OOFExternalAll   = 0 // every external sender
	OOFExternalKnown = 1 // only senders already in the mailbox's Contacts
)

// UnmarshalJSON decodes stored OOF settings, folding a pre-split blob's single
// "subject" into InternalSubject when the new "internalSubject" key is absent, so
// an out-of-office config saved before the subject was split per audience does
// not silently lose its reply subject on the first read after the upgrade.
func (c *OOFSettings) UnmarshalJSON(data []byte) error {
	type alias OOFSettings // a defined type without this method, so no recursion
	aux := struct {
		*alias
		LegacySubject *string `json:"subject"`
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if c.InternalSubject == "" && aux.LegacySubject != nil {
		c.InternalSubject = *aux.LegacySubject
	}
	return nil
}

// OOFActive reports whether an auto-reply should fire for a message delivered at
// nowUnix: OOF is enabled and the time falls within the configured window. An
// unset bound (0) is open-ended.
func (c OOFSettings) OOFActive(nowUnix int64) bool {
	if !c.Enabled {
		return false
	}
	if c.Start != 0 && nowUnix < c.Start {
		return false
	}
	if c.End != 0 && nowUnix > c.End {
		return false
	}
	return true
}

// GetOOFSettings returns the mailbox's out-of-office settings, or the zero value
// (disabled) when none are stored.
func (s *Store) GetOOFSettings() (OOFSettings, error) {
	var cfg OOFSettings
	props, err := s.GetStoreProperties(mapi.PrOOFSettings)
	if err != nil {
		return cfg, err
	}
	if v, ok := props.Get(mapi.PrOOFSettings); ok {
		if raw, ok := v.(string); ok && raw != "" {
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				return OOFSettings{}, err
			}
		}
	}
	return cfg, nil
}

// SetOOFSettings stores the mailbox's out-of-office settings and keeps the
// standard PR_OOF_STATE boolean in sync with Enabled, so a MAPI client and the
// delivery path can read the on/off state directly.
func (s *Store) SetOOFSettings(cfg OOFSettings) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrOOFSettings, Value: string(b)},
		{Tag: mapi.PrOOFState, Value: cfg.Enabled},
	})
}

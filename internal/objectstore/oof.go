package objectstore

import (
	"encoding/json"

	"hermex/internal/mapi"
)

// OOFSettings is a mailbox's out-of-office (automatic reply) configuration. An
// auto-reply fires at delivery time when OOFActive reports true for the
// delivery time. Internal and external senders may receive different bodies;
// external replies are sent only when ExternalEnabled.
type OOFSettings struct {
	Enabled         bool   `json:"enabled"`
	Subject         string `json:"subject"`
	InternalReply   string `json:"internalReply"`
	ExternalReply   string `json:"externalReply"`
	ExternalEnabled bool   `json:"externalEnabled"`
	Start           int64  `json:"start"` // unix seconds; 0 = no start bound
	End             int64  `json:"end"`   // unix seconds; 0 = no end bound
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

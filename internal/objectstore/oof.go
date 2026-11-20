package objectstore

import (
	"database/sql"
	"encoding/json"
	"errors"

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

// ErrInvalidOOFWindow refuses a schedule bound that is not a time, or a window
// that ends before it starts. A single unset bound (0) stays valid: OOFActive
// reads it as open-ended.
var ErrInvalidOOFWindow = errors.New("out-of-office window needs a start before its end")

// checkWindow reports whether each bound is unset or a time, and a window with
// both bounds ends after it starts.
func (c OOFSettings) checkWindow() error {
	if c.Start < 0 || c.End < 0 {
		return ErrInvalidOOFWindow
	}
	if c.Start != 0 && c.End != 0 && c.End <= c.Start {
		return ErrInvalidOOFWindow
	}
	return nil
}

// UpdateOOFSettings applies update to the stored out-of-office settings inside
// one write transaction, and keeps PR_OOF_STATE in step like SetOOFSettings. A
// protocol that sets only the fields its wire carries (EWS, ActiveSync) merges
// onto the stored value, so a write made elsewhere between the read and the
// write would otherwise be erased. update must not touch the store.
func (s *Store) UpdateOOFSettings(update func(*OOFSettings) error) error {
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	raw, err := s.txStoreString(tx, mapi.PrOOFSettings)
	if err != nil {
		return err
	}
	var cfg OOFSettings
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return err
		}
	}
	prev := cfg
	if err := update(&cfg); err != nil {
		return err
	}
	if err := cfg.checkWindow(); err != nil {
		return err
	}
	if err := s.txWriteOOF(tx, cfg); err != nil {
		return err
	}
	if err := s.txEndOOFPeriod(tx, raw != "" && prev == cfg); err != nil {
		return err
	}
	return tx.Commit()
}

// txEndOOFPeriod starts a new out-of-office period unless the settings were
// rewritten unchanged: a reply turned off, rescheduled or reworded is a new reply
// the senders already answered have not seen. EWS and ActiveSync clients re-send
// unchanged settings, and such a write keeps the period.
func (s *Store) txEndOOFPeriod(tx *sql.Tx, unchanged bool) error {
	if unchanged {
		return nil
	}
	return s.txDropAutoReplyList(tx, AutoReplyListOOF)
}

// txWriteOOF stores cfg and its PR_OOF_STATE inside tx.
func (s *Store) txWriteOOF(tx *sql.Tx, cfg OOFSettings) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	for _, p := range []mapi.TaggedPropVal{
		{Tag: mapi.PrOOFSettings, Value: string(b)},
		{Tag: mapi.PrOOFState, Value: cfg.Enabled},
	} {
		enc, err := s.storedPropval(p.Tag, p.Value)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO store_properties (proptag, propval) VALUES (?, ?)
			 ON CONFLICT(proptag) DO UPDATE SET propval = excluded.propval`,
			int64(uint32(p.Tag)), enc); err != nil {
			return err
		}
	}
	return nil
}

// SetOOFSettings stores the mailbox's out-of-office settings and keeps the
// standard PR_OOF_STATE boolean in sync with Enabled, so a MAPI client and the
// delivery path can read the on/off state directly.
// A stored value that no longer decodes is replaced, not refused: this call sets
// the whole value.
func (s *Store) SetOOFSettings(cfg OOFSettings) error {
	if err := cfg.checkWindow(); err != nil {
		return err
	}
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	raw, err := s.txStoreString(tx, mapi.PrOOFSettings)
	if err != nil {
		return err
	}
	var prev OOFSettings
	unchanged := raw != "" && json.Unmarshal([]byte(raw), &prev) == nil && prev == cfg
	if err := s.txWriteOOF(tx, cfg); err != nil {
		return err
	}
	if err := s.txEndOOFPeriod(tx, unchanged); err != nil {
		return err
	}
	return tx.Commit()
}

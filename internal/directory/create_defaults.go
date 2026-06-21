package directory

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// CreateDefaults holds the default parameters pre-filled into the create forms for
// a new domain or user. The system-wide set (scope 0) is the base; a per-domain
// set (scope = domain id) overlays the USER defaults for accounts in that domain.
type CreateDefaults struct {
	Domain DomainCreateDefaults `json:"domain"`
	User   UserCreateDefaults   `json:"user"`
}

// DomainCreateDefaults seeds the new-domain form. It is meaningful only at the
// system scope (a domain is created at the system level, not within a domain).
type DomainCreateDefaults struct {
	MaxUser int64 `json:"maxUser,omitempty"`
}

// UserCreateDefaults seeds the new-user form. Every field is a pointer so a
// per-domain override can set just some fields and inherit the rest from the
// system layer (and ultimately the built-in default). Quotas are in KiB.
type UserCreateDefaults struct {
	Lang      *string `json:"lang,omitempty"`
	POP3IMAP  *bool   `json:"pop3_imap,omitempty"`
	SMTP      *bool   `json:"smtp,omitempty"`
	ChgPasswd *bool   `json:"changePassword,omitempty"`
	Web       *bool   `json:"web,omitempty"`
	EAS       *bool   `json:"eas,omitempty"`
	DAV       *bool   `json:"dav,omitempty"`
	StorageKB *int64  `json:"storageQuota,omitempty"`
	ReceiveKB *int64  `json:"receiveQuota,omitempty"`
	SendKB    *int64  `json:"sendQuota,omitempty"`
}

// ResolvedUserDefaults is the concrete user-create defaults after merging the
// system and per-domain layers and filling every unset field with the built-in
// default. It is what the new-user form is pre-filled from.
type ResolvedUserDefaults struct {
	Lang      string
	POP3IMAP  bool
	SMTP      bool
	ChgPasswd bool
	Web       bool
	EAS       bool
	DAV       bool
	StorageKB int64
	ReceiveKB int64
	SendKB    int64
}

// builtinUserDefaults is the baseline an empty defaults configuration resolves to —
// it mirrors what CreateUser provisions when no defaults are stored (POP3/IMAP,
// SMTP, and the DETAIL1-opt-out services web/EAS/DAV on; password change off; no
// language; unlimited quotas), so pre-fill matches an unconfigured create.
func builtinUserDefaults() ResolvedUserDefaults {
	return ResolvedUserDefaults{POP3IMAP: true, SMTP: true, Web: true, EAS: true, DAV: true}
}

// GetCreateDefaults returns the stored defaults for a scope (0 = system-wide, a
// domain id = that domain's override), or ok=false when none is stored.
func (d *SQLDirectory) GetCreateDefaults(scopeID int64) (CreateDefaults, bool, error) {
	var raw string
	err := d.db.QueryRow(`SELECT params FROM create_defaults WHERE scope_id = ?`, scopeID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return CreateDefaults{}, false, nil
	}
	if err != nil {
		return CreateDefaults{}, false, err
	}
	var cd CreateDefaults
	if err := json.Unmarshal([]byte(raw), &cd); err != nil {
		return CreateDefaults{}, false, err
	}
	return cd, true, nil
}

// SetCreateDefaults stores the defaults for a scope, replacing any prior value.
func (d *SQLDirectory) SetCreateDefaults(scopeID int64, cd CreateDefaults) error {
	b, err := json.Marshal(cd)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`REPLACE INTO create_defaults (scope_id, params) VALUES (?, ?)`, scopeID, string(b))
	return err
}

// DeleteCreateDefaults removes a scope's stored defaults, reporting whether a row
// existed. Used to clear a per-domain override so it falls back to the system set.
func (d *SQLDirectory) DeleteCreateDefaults(scopeID int64) (bool, error) {
	res, err := d.db.Exec(`DELETE FROM create_defaults WHERE scope_id = ?`, scopeID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// EffectiveUserDefaults resolves the user-create defaults for a domain: the
// built-in baseline, the system-wide layer over it, then the domain's override on
// top. A nil field at a layer leaves the layer below showing through.
func (d *SQLDirectory) EffectiveUserDefaults(domainID int64) (ResolvedUserDefaults, error) {
	res := builtinUserDefaults()
	sys, ok, err := d.GetCreateDefaults(0)
	if err != nil {
		return res, err
	}
	if ok {
		overlayUserDefaults(&res, sys.User)
	}
	if domainID != 0 {
		dom, ok, err := d.GetCreateDefaults(domainID)
		if err != nil {
			return res, err
		}
		if ok {
			overlayUserDefaults(&res, dom.User)
		}
	}
	return res, nil
}

// overlayUserDefaults applies a layer's set (non-nil) fields onto the resolved set.
func overlayUserDefaults(res *ResolvedUserDefaults, u UserCreateDefaults) {
	if u.Lang != nil {
		res.Lang = *u.Lang
	}
	if u.POP3IMAP != nil {
		res.POP3IMAP = *u.POP3IMAP
	}
	if u.SMTP != nil {
		res.SMTP = *u.SMTP
	}
	if u.ChgPasswd != nil {
		res.ChgPasswd = *u.ChgPasswd
	}
	if u.Web != nil {
		res.Web = *u.Web
	}
	if u.EAS != nil {
		res.EAS = *u.EAS
	}
	if u.DAV != nil {
		res.DAV = *u.DAV
	}
	if u.StorageKB != nil {
		res.StorageKB = *u.StorageKB
	}
	if u.ReceiveKB != nil {
		res.ReceiveKB = *u.ReceiveKB
	}
	if u.SendKB != nil {
		res.SendKB = *u.SendKB
	}
}

package directory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"hermex/internal/easpolicy"
)

// defaultSyncPolicyOrg is the org id of the single server-wide default policy row.
const defaultSyncPolicyOrg = 0

// GetDefaultSyncPolicy returns the server-wide default ActiveSync device policy, or nil
// when none has been configured (no enforcement by default).
func (d *SQLDirectory) GetDefaultSyncPolicy() (easpolicy.Policy, error) {
	var raw string
	err := d.db.QueryRow(`SELECT policy FROM sync_policy WHERE org_id = ?`, defaultSyncPolicyOrg).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || raw == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p easpolicy.Policy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	if len(p) == 0 {
		return nil, nil // a stored empty object means "no default"
	}
	return p, nil
}

// SetDefaultSyncPolicy replaces the server-wide default device policy. An empty policy
// clears it (a later read returns nil and no policy is enforced by default).
func (d *SQLDirectory) SetDefaultSyncPolicy(p easpolicy.Policy) error {
	raw := ""
	if len(p) > 0 {
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	_, err := d.db.Exec(`REPLACE INTO sync_policy (org_id, policy) VALUES (?, ?)`, defaultSyncPolicyOrg, raw)
	return err
}

// GetDomainSyncPolicy returns a domain's device-policy override (keyed by domain
// name, the form the provisioning path holds), or nil when the domain sets none.
// It is the middle inheritance layer: the server default is merged under it and a
// per-user override over it.
func (d *SQLDirectory) GetDomainSyncPolicy(domain string) (easpolicy.Policy, error) {
	var raw sql.NullString
	err := d.db.QueryRow(`SELECT sync_policy FROM domains WHERE domainname = ?`,
		strings.ToLower(strings.TrimSpace(domain))).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var p easpolicy.Policy
	if err := json.Unmarshal([]byte(raw.String), &p); err != nil {
		return nil, err
	}
	if len(p) == 0 {
		return nil, nil
	}
	return p, nil
}

// SetDomainSyncPolicy replaces a domain's device-policy override (keyed by domain
// name), reporting whether the domain existed. An empty policy clears the override
// so the domain falls back to the server default. Existence is checked first so an
// idempotent write (the affected-row count would be 0) is not read as not found.
func (d *SQLDirectory) SetDomainSyncPolicy(domain string, p easpolicy.Policy) (bool, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	var one int64
	err := d.db.QueryRow(`SELECT id FROM domains WHERE domainname = ?`, domain).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	raw := ""
	if len(p) > 0 {
		b, err := json.Marshal(p)
		if err != nil {
			return false, err
		}
		raw = string(b)
	}
	_, err = d.db.Exec(`UPDATE domains SET sync_policy = ? WHERE domainname = ?`, raw, domain)
	return err == nil, err
}

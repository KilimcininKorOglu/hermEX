package directory

import (
	"database/sql"
	"encoding/json"
	"errors"

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

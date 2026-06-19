package objectstore

import (
	"database/sql"
	"errors"
	"fmt"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// ruleExtFlags freezes the encoding used for the condition (RESTRICTION) and
// actions (RULE_ACTIONS) blobs stored in the rules table. Zero flags select the
// internal form — UTF-8 strings and 16-bit AND/OR child counts — and the same
// flags must be used to read a blob back. It mirrors propExtFlags (value.go).
const ruleExtFlags = ext.Flags(0)

// ruleProviderDefault is the PR_RULE_PROVIDER value stamped on rules created
// through this API when the caller leaves Provider empty. "RuleOrganizer" is the
// standard provider for the user-managed rule set — the rules a desktop client's
// Rules manager creates and edits.
const ruleProviderDefault = "RuleOrganizer"

// Rule is one stored mailbox rule: its identity, evaluation order, state
// bitmask (PR_RULE_STATE), and the condition/action pair. Condition is the
// RESTRICTION tested against a message; Actions is the RULE_ACTIONS applied when
// the condition matches.
type Rule struct {
	ID        int64
	FolderID  int64
	Name      string
	Provider  string
	Sequence  int32
	State     uint32
	Condition mapi.Restriction
	Actions   mapi.RuleActions
}

// Enabled reports whether the rule's ST_ENABLED bit is set.
func (r Rule) Enabled() bool { return r.State&mapi.RuleStateEnabled != 0 }

// AddRule stores a new rule on a folder and returns its assigned rule_id. The
// condition and actions are serialized to their RESTRICTION / RULE_ACTIONS forms
// before storage. Sequence orders evaluation; when the caller passes a
// non-positive value the rule is appended after the folder's current highest
// sequence. Provider defaults to the standard user-rule provider when empty.
func (s *Store) AddRule(r Rule) (int64, error) {
	cond := ext.NewPush(ruleExtFlags)
	if err := cond.Restriction(r.Condition); err != nil {
		return 0, err
	}
	acts := ext.NewPush(ruleExtFlags)
	if err := acts.RuleActions(r.Actions); err != nil {
		return 0, err
	}
	provider := r.Provider
	if provider == "" {
		provider = ruleProviderDefault
	}

	tx, err := s.objdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	seq := r.Sequence
	if seq <= 0 {
		var max sql.NullInt64
		if err := tx.QueryRow(
			`SELECT MAX(sequence) FROM rules WHERE folder_id=?`, r.FolderID).Scan(&max); err != nil {
			return 0, err
		}
		seq = int32(max.Int64) + 1
	}
	res, err := tx.Exec(
		`INSERT INTO rules (name, provider, sequence, state, condition, actions, folder_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Name, provider, seq, int64(r.State), cond.Bytes(), acts.Bytes(), r.FolderID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// ListRules returns a folder's rules in evaluation order (by sequence, then
// rule_id for a stable tiebreak), decoding each stored RESTRICTION and
// RULE_ACTIONS blob. A folder with no rules yields an empty slice.
func (s *Store) ListRules(folderID int64) ([]Rule, error) {
	rows, err := s.objdb.Query(
		`SELECT rule_id, name, provider, sequence, state, condition, actions
		 FROM rules WHERE folder_id=? ORDER BY sequence, rule_id`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var (
			r        Rule
			name     sql.NullString
			state    int64
			condBlob []byte
			actBlob  []byte
		)
		if err := rows.Scan(&r.ID, &name, &r.Provider, &r.Sequence, &state, &condBlob, &actBlob); err != nil {
			return nil, err
		}
		r.FolderID = folderID
		r.Name = name.String
		r.State = uint32(state)
		if r.Condition, err = ext.NewPull(condBlob, ruleExtFlags).Restriction(); err != nil {
			return nil, err
		}
		if r.Actions, err = ext.NewPull(actBlob, ruleExtFlags).RuleActions(); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRuleEnabled toggles a rule's ST_ENABLED bit, leaving its other state bits
// untouched, and reports ErrNotFound when no such rule exists. A disabled rule
// is kept in the table and skipped during evaluation rather than deleted.
func (s *Store) SetRuleEnabled(ruleID int64, enabled bool) error {
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var state int64
	err = tx.QueryRow(`SELECT state FROM rules WHERE rule_id=?`, ruleID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if enabled {
		state |= int64(mapi.RuleStateEnabled)
	} else {
		state &^= int64(mapi.RuleStateEnabled)
	}
	if _, err := tx.Exec(`UPDATE rules SET state=? WHERE rule_id=?`, state, ruleID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteRule removes a rule by id, reporting ErrNotFound when no such rule
// exists.
func (s *Store) DeleteRule(ruleID int64) error {
	res, err := s.objdb.Exec(`DELETE FROM rules WHERE rule_id=?`, ruleID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RuleOp is a decoded RopModifyRules row operation (the RuleData flags). The zero
// value is invalid so a half-built change is rejected rather than treated as an add.
type RuleOp uint8

const (
	RuleAdd    RuleOp = iota + 1 // ROW_ADD: store a new rule
	RuleModify                   // ROW_MODIFY: update an existing rule's present columns
	RuleRemove                   // ROW_REMOVE: drop a rule by id
)

// RulePatch is a RopModifyRules row's rule columns, with a nil pointer meaning the
// property was absent from the wire row. An Add carries the rule's required columns; a
// Modify carries only the columns the client actually sent — the store updates those
// and leaves the rest unchanged, mirroring the reference's present-only merge (an
// omitted column is never wiped). hermEX models the six columns its rules table holds;
// the reference's level/user_flags/provider_data are not stored.
type RulePatch struct {
	Name      *string
	Provider  *string
	Sequence  *int32
	State     *uint32
	Condition *mapi.Restriction
	Actions   *mapi.RuleActions
}

// RuleChange is one decoded RopModifyRules row. RuleID is the PR_RULE_ID a Modify or
// Remove targets (the client got it from RopGetRulesTable); an Add ignores it and the
// store assigns the id. Patch carries the row's properties.
type RuleChange struct {
	Op     RuleOp
	RuleID int64
	Patch  RulePatch
}

// ModifyRules applies a decoded RopModifyRules batch to a folder in one transaction.
// When replace is set the folder's whole rule set is cleared first (the REPLACE flag,
// under which the wire carries only adds). An Add inserts a new rule; a Modify updates
// the present columns of the rule with the given id (only when it belongs to the
// folder); a Remove drops it. A Modify or Remove of an absent or foreign rule is a
// no-op, not a fault — the reference tolerates a stale id within a batch.
func (s *Store) ModifyRules(folderID int64, replace bool, changes []RuleChange) error {
	tx, err := s.objdb.Begin()
	if err != nil {
		s.logStoreError("modify-rules", err)
		return err
	}
	defer tx.Rollback()

	if replace {
		if _, err := tx.Exec(`DELETE FROM rules WHERE folder_id=?`, folderID); err != nil {
			s.logStoreError("modify-rules", err)
			return err
		}
	}

	for _, c := range changes {
		switch c.Op {
		case RuleAdd:
			err = insertRulePatch(tx, folderID, c.Patch)
		case RuleModify:
			err = updateRulePatch(tx, folderID, c.RuleID, c.Patch)
		case RuleRemove:
			_, err = tx.Exec(`DELETE FROM rules WHERE folder_id=? AND rule_id=?`, folderID, c.RuleID)
		default:
			return fmt.Errorf("objectstore: unknown rule op %d", c.Op)
		}
		if err != nil {
			s.logStoreError("modify-rules", err)
			return err
		}
	}
	return tx.Commit()
}

// insertRulePatch inserts a new rule from an Add row's patch: provider defaults to the
// standard user-rule provider, a non-positive sequence appends after the folder's
// highest, and an absent condition/actions stores as the empty form (matching a direct
// AddRule of a zero-valued Rule).
func insertRulePatch(tx *sql.Tx, folderID int64, p RulePatch) error {
	cond, err := marshalRestriction(p.Condition)
	if err != nil {
		return err
	}
	acts, err := marshalRuleActions(p.Actions)
	if err != nil {
		return err
	}
	provider := ruleProviderDefault
	if p.Provider != nil && *p.Provider != "" {
		provider = *p.Provider
	}
	var name string
	if p.Name != nil {
		name = *p.Name
	}
	var state uint32
	if p.State != nil {
		state = *p.State
	}
	var seq int32
	if p.Sequence != nil {
		seq = *p.Sequence
	}
	if seq <= 0 {
		var max sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(sequence) FROM rules WHERE folder_id=?`, folderID).Scan(&max); err != nil {
			return err
		}
		seq = int32(max.Int64) + 1
	}
	_, err = tx.Exec(
		`INSERT INTO rules (name, provider, sequence, state, condition, actions, folder_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		name, provider, seq, int64(state), cond, acts, folderID)
	return err
}

// updateRulePatch updates only the columns a Modify row carried, keyed by (folder_id,
// rule_id) — the reference's per-column present-only merge. It first confirms the rule
// belongs to the folder; a missing or foreign rule is a silent no-op. (hermEX also
// honors a present PR_RULE_NAME on modify, which the reference omits — a harmless
// superset, since a client sending the name expects it applied.)
func updateRulePatch(tx *sql.Tx, folderID, ruleID int64, p RulePatch) error {
	var owner int64
	switch err := tx.QueryRow(`SELECT folder_id FROM rules WHERE rule_id=?`, ruleID).Scan(&owner); {
	case errors.Is(err, sql.ErrNoRows):
		return nil // absent rule: skip, matching the reference
	case err != nil:
		return err
	case owner != folderID:
		return nil // foreign rule: skip
	}

	if p.Name != nil {
		if _, err := tx.Exec(`UPDATE rules SET name=? WHERE rule_id=?`, *p.Name, ruleID); err != nil {
			return err
		}
	}
	if p.Provider != nil {
		if _, err := tx.Exec(`UPDATE rules SET provider=? WHERE rule_id=?`, *p.Provider, ruleID); err != nil {
			return err
		}
	}
	if p.Sequence != nil {
		if _, err := tx.Exec(`UPDATE rules SET sequence=? WHERE rule_id=?`, *p.Sequence, ruleID); err != nil {
			return err
		}
	}
	if p.State != nil {
		if _, err := tx.Exec(`UPDATE rules SET state=? WHERE rule_id=?`, int64(*p.State), ruleID); err != nil {
			return err
		}
	}
	if p.Condition != nil {
		blob, err := marshalRestriction(p.Condition)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE rules SET condition=? WHERE rule_id=?`, blob, ruleID); err != nil {
			return err
		}
	}
	if p.Actions != nil {
		blob, err := marshalRuleActions(p.Actions)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE rules SET actions=? WHERE rule_id=?`, blob, ruleID); err != nil {
			return err
		}
	}
	return nil
}

// marshalRestriction serializes a rule condition to its stored blob; a nil pointer
// stores the empty RESTRICTION form (the zero value, as AddRule does for an unset
// condition).
func marshalRestriction(r *mapi.Restriction) ([]byte, error) {
	var v mapi.Restriction
	if r != nil {
		v = *r
	}
	p := ext.NewPush(ruleExtFlags)
	if err := p.Restriction(v); err != nil {
		return nil, err
	}
	return p.Bytes(), nil
}

// marshalRuleActions serializes rule actions to their stored blob; a nil pointer
// stores the empty RULE_ACTIONS form.
func marshalRuleActions(a *mapi.RuleActions) ([]byte, error) {
	var v mapi.RuleActions
	if a != nil {
		v = *a
	}
	p := ext.NewPush(ruleExtFlags)
	if err := p.RuleActions(v); err != nil {
		return nil, err
	}
	return p.Bytes(), nil
}

package objectstore

import (
	"database/sql"
	"errors"

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

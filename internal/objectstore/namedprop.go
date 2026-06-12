package objectstore

import (
	"database/sql"
	"fmt"
	"strconv"

	"hermex/internal/mapi"
)

// Named-property id range (MS-OXCDATA §2.6.1): named properties are numbered
// from namedPropBase upward. The base is enforced by the allocator so a fresh
// store hands out ids in this range without any pre-seed.
const (
	namedPropBase uint64 = 0x8000
	namedPropMax  uint64 = 0xFFFE
)

// namedPropKey builds the canonical string key for a named property: the GUID
// namespace followed by either the long id or the name. Identical names always
// map to the same key, which the unique index on name_string then dedups.
// Returns ok=false for an unrepresentable name (unknown kind or an over-long
// name string), which the caller maps to propid 0.
func namedPropKey(n mapi.PropertyName) (string, bool) {
	switch n.Kind {
	case mapi.MnidID:
		return "GUID=" + n.GUID.String() + ",LID=" + strconv.FormatUint(uint64(n.LID), 10), true
	case mapi.MnidString:
		if len(n.Name) >= 1024 {
			return "", false
		}
		return "GUID=" + n.GUID.String() + ",NAME=" + n.Name, true
	default:
		return "", false
	}
}

// GetNamedPropIDs resolves named properties to their store property ids,
// allocating new ids (when create is true) for names not seen before. The
// result is parallel to names; an unknown name with create false, or an
// unrepresentable name, maps to 0. Allocated ids are stable across reopen.
func (s *Store) GetNamedPropIDs(create bool, names []mapi.PropertyName) ([]uint16, error) {
	tx, err := s.objdb.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids, err := getNamedPropIDs(tx, create, names)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// getNamedPropIDs is the transaction-scoped resolver, so message import can
// resolve and allocate named-property ids inside its own transaction.
func getNamedPropIDs(q sqlExec, create bool, names []mapi.PropertyName) ([]uint16, error) {
	ids := make([]uint16, len(names))
	for i, n := range names {
		key, ok := namedPropKey(n)
		if !ok {
			ids[i] = 0
			continue
		}
		var propid uint16
		err := q.QueryRow(`SELECT propid FROM named_properties WHERE name_string=?`, key).Scan(&propid)
		if err == nil {
			ids[i] = propid
			continue
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
		if !create {
			ids[i] = 0
			continue
		}
		// Allocate the next id, computing it explicitly so the floor is
		// namedPropBase even on an empty table (no pre-seed needed).
		var maxID uint64
		if err := q.QueryRow(`SELECT COALESCE(MAX(propid), ?) FROM named_properties`, int64(namedPropBase-1)).Scan(&maxID); err != nil {
			return nil, err
		}
		next := maxID + 1
		if next > namedPropMax {
			return nil, fmt.Errorf("objectstore: named-property id space exhausted")
		}
		if _, err := q.Exec(`INSERT INTO named_properties (propid, name_string) VALUES (?, ?)`, int64(next), key); err != nil {
			return nil, err
		}
		ids[i] = uint16(next)
	}
	return ids, nil
}

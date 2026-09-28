package objectstore

import (
	"database/sql"
	"errors"
	"fmt"

	"hermex/internal/mapi"
)

// foreignNamedPropQuota bounds how many named-property ids a store allocates for
// names a message's sender chose, such as the properties a TNEF stream carries.
// Every name is one id of a store-wide space of about 32,000, and without a bound
// mail carrying invented names would spend it and stop every later allocation,
// delivery's included.
const foreignNamedPropQuota = 2000

// errForeignNamedPropQuota names why a sender-chosen name was left unresolved.
var errForeignNamedPropQuota = errors.New("objectstore: sender-chosen named property quota reached")

// GetForeignNamedPropIDs resolves names a message's sender chose. A name the store
// already knows resolves as GetNamedPropIDs resolves it; a new one is allocated
// only while the store's quota of such names lasts, and maps to 0 afterwards. The
// result is parallel to names.
func (s *Store) GetForeignNamedPropIDs(names []mapi.PropertyName) ([]uint16, error) {
	tx, err := s.objdb.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids := make([]uint16, len(names))
	declined := 0
	for i, n := range names {
		id, err := foreignNamedPropID(tx, n)
		if err != nil {
			return nil, err
		}
		if id == 0 {
			declined++
		}
		ids[i] = id
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if declined > 0 {
		s.LogSwallowedError("objectstore.foreign_named_prop", fmt.Errorf("%w: %d declined", errForeignNamedPropQuota, declined))
	}
	return ids, nil
}

// foreignNamedPropID resolves one sender-chosen name inside a transaction.
func foreignNamedPropID(q sqlExec, n mapi.PropertyName) (uint16, error) {
	key, ok := namedPropKey(n)
	if !ok {
		return 0, nil
	}
	var propid uint16
	err := q.QueryRow(`SELECT propid FROM named_properties WHERE name_string=?`, key).Scan(&propid)
	if err == nil {
		return propid, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	var used int
	if err := q.QueryRow(`SELECT COUNT(*) FROM foreign_named_props`).Scan(&used); err != nil {
		return 0, err
	}
	if used >= foreignNamedPropQuota {
		return 0, nil
	}
	id, err := allocateNamedProp(q, n, key)
	if err != nil || id == 0 {
		return id, err
	}
	if _, err := q.Exec(`INSERT INTO foreign_named_props (propid) VALUES (?)`, int64(id)); err != nil {
		return 0, err
	}
	return id, nil
}

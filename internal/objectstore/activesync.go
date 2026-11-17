package objectstore

import (
	"database/sql"
	"errors"
	"fmt"

	"hermex/internal/mapi"
)

// UpdateActiveSyncState applies update to the stored ActiveSync sync-state JSON
// inside one write transaction, so a concurrent writer, in this process or
// another, cannot slip a write in between the read and the write and have it
// erased. See updateStoreString.
func (s *Store) UpdateActiveSyncState(update func(stateJSON string) (string, bool, error)) error {
	return s.updateStoreString(mapi.PrActiveSyncState, update)
}

// UpdateActiveSyncDevices applies update to the stored ActiveSync device-metadata
// JSON inside one write transaction, like UpdateActiveSyncState.
func (s *Store) UpdateActiveSyncDevices(update func(devicesJSON string) (string, bool, error)) error {
	return s.updateStoreString(mapi.PrActiveSyncDevices, update)
}

// updateStoreString reads one string store property, passes it to update and
// writes the result, all in one transaction. The store opens every transaction
// with the write lock (_txlock=immediate), so writers queue on the lock rather
// than each overwriting a value it read before the other's commit. update reports
// whether to write; it must not touch the store, because a store write from
// inside it would wait for the lock this transaction holds. The tag must be an
// inline property, not a content-offloaded one, so its whole value is in the
// transaction.
func (s *Store) updateStoreString(tag mapi.PropTag, update func(string) (string, bool, error)) error {
	if isCIDProp(tag) {
		return fmt.Errorf("objectstore: %s is stored outside the property table", tag)
	}
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cur, err := s.txStoreString(tx, tag)
	if err != nil {
		return err
	}
	next, write, err := update(cur)
	if err != nil || !write {
		return err
	}
	enc, err := s.storedPropval(tag, next)
	if err != nil {
		return fmt.Errorf("objectstore: encode %s: %w", tag, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO store_properties (proptag, propval) VALUES (?, ?)
		 ON CONFLICT(proptag) DO UPDATE SET propval = excluded.propval`,
		int64(uint32(tag)), enc); err != nil {
		return err
	}
	return tx.Commit()
}

// txStoreString reads one string store property inside tx, "" when absent.
func (s *Store) txStoreString(tx *sql.Tx, tag mapi.PropTag) (string, error) {
	var col any
	err := tx.QueryRow(`SELECT propval FROM store_properties WHERE proptag=?`, int64(uint32(tag))).Scan(&col)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	v, err := s.loadPropval(tag, col)
	if err != nil {
		return "", fmt.Errorf("objectstore: decode %s: %w", tag, err)
	}
	str, _ := v.(string)
	return str, nil
}

// GetActiveSyncState returns the ActiveSync sync-state JSON stored on the store
// root, or the empty string when no device has synced yet. The state lives as a
// single MAPI property (PrActiveSyncState) rather than in a dedicated table,
// the same "everything is a property" shape the rest of the store uses, mirroring
// the webmail-settings and out-of-office stores.
func (s *Store) GetActiveSyncState() (string, error) {
	props, err := s.GetStoreProperties(mapi.PrActiveSyncState)
	if err != nil {
		return "", err
	}
	if v, ok := props.Get(mapi.PrActiveSyncState); ok {
		if str, ok := v.(string); ok {
			return str, nil
		}
	}
	return "", nil
}

// SetActiveSyncState saves the ActiveSync sync-state JSON as the store-root
// property, replacing any previous value.
func (s *Store) SetActiveSyncState(stateJSON string) error {
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrActiveSyncState, Value: stateJSON},
	})
}

// GetActiveSyncDevices returns the ActiveSync device-metadata JSON stored on the
// store root, or the empty string when no device has been recorded yet. It is a
// sibling property of PrActiveSyncState: device metadata (type, agent, last seen,
// wipe status) lives apart from the sync-state blob so a best-effort metadata
// write cannot clobber a concurrent sync-key update.
func (s *Store) GetActiveSyncDevices() (string, error) {
	props, err := s.GetStoreProperties(mapi.PrActiveSyncDevices)
	if err != nil {
		return "", err
	}
	if v, ok := props.Get(mapi.PrActiveSyncDevices); ok {
		if str, ok := v.(string); ok {
			return str, nil
		}
	}
	return "", nil
}

// SetActiveSyncDevices saves the ActiveSync device-metadata JSON as the store-root
// property, replacing any previous value.
func (s *Store) SetActiveSyncDevices(devicesJSON string) error {
	return s.SetStoreProperties(mapi.PropertyValues{
		{Tag: mapi.PrActiveSyncDevices, Value: devicesJSON},
	})
}

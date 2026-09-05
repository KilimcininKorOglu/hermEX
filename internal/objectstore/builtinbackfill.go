package objectstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/migrate"
)

// migrateAndBackfill carries a store forward: it applies any migration beyond
// the baseline, then restores a private mailbox's missing default folders. Both
// steps run on every Open, for a fresh store and an existing one alike.
func (s *Store) migrateAndBackfill() error {
	if err := migrate.Run(context.Background(), s.objectDriver(), objectSchemaVersion, objectMigrations); err != nil {
		return err
	}
	// The public store keeps its own smaller hierarchy, and a store opened
	// without seeding is deliberately bare.
	if !s.seedBuiltins || s.kind == storePublic {
		return nil
	}
	return s.backfillBuiltinFolders()
}

// backfillBuiltinFolders creates any default folder a mailbox is missing. A
// mailbox provisioned before a folder joined the default set has no row for it,
// and a client that asks for that folder by its distinguished name would be told
// the mailbox does not have one.
//
// It cannot be a SQL migration: a folder row carries a change key stamped with
// the store's own replica GUID and a message-id range carved from the store's
// counters, neither of which a static statement can produce.
//
// It runs on every Open of a private mailbox and costs one indexed read when
// nothing is missing, which is the ordinary case.
func (s *Store) backfillBuiltinFolders() error {
	missing, err := s.missingBuiltins()
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	replica, err := s.replicaGUID()
	if err != nil {
		return err
	}
	tx, err := s.objdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // the commit below is the success path
	ntNow := mapi.UnixToNTTime(time.Now())
	for _, f := range missing {
		// A folder whose parent is also missing is created after it, because the
		// list is in creation order and the parent comes first.
		if err := createGenericFolder(tx, replica, ntNow, f); err != nil {
			return fmt.Errorf("objectstore: backfill folder %#x: %w", f.fid, err)
		}
	}
	return tx.Commit()
}

// missingBuiltins returns the default folders this mailbox does not hold and
// can safely gain.
func (s *Store) missingBuiltins() ([]builtinFolder, error) {
	have, err := s.existingFolderIDs()
	if err != nil {
		return nil, err
	}
	taken, err := s.folderNamesByParent()
	if err != nil {
		return nil, err
	}
	var missing []builtinFolder
	for _, f := range builtinFolders {
		// A user who already made their own folder under that name keeps it. A
		// second folder with the same name in the same parent would leave every
		// name-based lookup picking one of two at random, and the user's own mail
		// is in one of them.
		if have[f.fid] || taken[folderName{f.parent, f.dispName}] {
			continue
		}
		missing = append(missing, f)
	}
	return missing, nil
}

// folderName keys a folder by its parent and display name, which is what makes
// two folders collide for a client that resolves by name.
type folderName struct {
	parent uint64
	name   string
}

// folderNamesByParent returns the (parent, display name) pairs the store already
// holds.
func (s *Store) folderNamesByParent() (map[folderName]bool, error) {
	rows, err := s.objdb.Query(
		`SELECT f.parent_id, p.propval FROM folders f
		   JOIN folder_properties p ON p.folder_id = f.folder_id AND p.proptag = ?`,
		int64(uint32(mapi.PrDisplayName)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	taken := map[folderName]bool{}
	for rows.Next() {
		var parent sql.NullInt64
		var enc []byte
		if err := rows.Scan(&parent, &enc); err != nil {
			return nil, err
		}
		v, err := decodeValue(mapi.PrDisplayName.Type(), enc)
		if err != nil {
			continue // a name this store cannot decode cannot collide by name either
		}
		name, _ := v.(string)
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		taken[folderName{uint64(parent.Int64), name}] = true
	}
	return taken, rows.Err()
}

// existingFolderIDs returns the set of folder ids the store already holds.
func (s *Store) existingFolderIDs() (map[uint64]bool, error) {
	rows, err := s.objdb.Query(`SELECT folder_id FROM folders`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[uint64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		have[uint64(id)] = true
	}
	return have, rows.Err()
}

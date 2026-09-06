package objectstore

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// ReindexFolder reconciles the IMAP index for a folder with the object store.
// It indexes every non-deleted object message that has no index row (the
// crash-gap case: the object committed but the index did not) and prunes every
// index row whose object is gone (a delete interrupted after the object was
// removed). Existing UID assignments are preserved, so UIDVALIDITY is not
// disturbed; a newly indexed message receives a fresh monotonic UID and a
// freshly re-synthesized eml.
func (s *Store) ReindexFolder(folderID int64) error {
	objState, err := scanInt64Map[int](s.objdb,
		`SELECT message_id, read_state FROM messages WHERE parent_fid=? AND is_deleted=0`, folderID)
	if err != nil {
		return err
	}
	idxMid, err := scanInt64Map[string](s.idxdb,
		`SELECT message_id, mid_string FROM messages WHERE folder_id=?`, folderID)
	if err != nil {
		return err
	}

	// Prune index rows whose object is gone.
	for id, mid := range idxMid {
		if _, ok := objState[id]; ok {
			continue
		}
		if err := s.dropIndexRow(id, mid); err != nil {
			return err
		}
	}

	// Index object messages missing from the index.
	for id, read := range objState {
		if _, ok := idxMid[id]; ok {
			continue
		}
		if err := s.indexExistingMessage(folderID, id, read != 0); err != nil {
			return err
		}
	}
	return nil
}

// scanInt64Map reads a two-column query into a map keyed by the first column.
func scanInt64Map[V any](db *sql.DB, query string, args ...any) (map[int64]V, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]V{}
	for rows.Next() {
		var k int64
		var v V
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// dropIndexRow removes one index row and its cached eml, for a message the
// object store no longer holds.
func (s *Store) dropIndexRow(id int64, mid string) error {
	if _, err := s.idxdb.Exec(`DELETE FROM messages WHERE message_id=?`, id); err != nil {
		return err
	}
	if _, err := s.idxdb.Exec(`DELETE FROM mapping WHERE message_id=?`, id); err != nil {
		return err
	}
	_ = os.Remove(s.emlPath(mid))
	return nil
}

// indexExistingMessage re-derives a stored message's wire form and index row, for
// a message the object store holds but the index has lost.
func (s *Store) indexExistingMessage(folderID, id int64, read bool) error {
	msg, err := s.OpenMessage(id)
	if err != nil {
		return err
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	mid := midString(uint64(id))
	eml, err := oxcmail.Export(msg, oxcmail.Options{Resolver: s.GetNamedPropIDs})
	if err != nil {
		return fmt.Errorf("objectstore: export: %w", err)
	}
	if err := s.writeEML(mid, eml); err != nil {
		return err
	}
	var flags int64
	if read {
		flags |= FlagSeen
	}
	_, err = s.indexMessage(folderID, id, mid, msg, int64(len(eml)), deliveryTime(msg.Props), flags)
	return err
}

// deliveryTime reads a message's delivery time for the index, falling back to
// now when the object carries none.
func deliveryTime(props mapi.PropertyValues) time.Time {
	if v, ok := props.Get(mapi.PrMessageDeliveryTime); ok {
		if nt, ok := v.(uint64); ok {
			return mapi.NTTimeToUnix(nt)
		}
	}
	return time.Now()
}

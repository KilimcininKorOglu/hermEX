package objectstore

import (
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
	// Object messages currently in the folder (id -> read_state).
	objState := map[int64]int{}
	objRows, err := s.objdb.Query(
		`SELECT message_id, read_state FROM messages WHERE parent_fid=? AND is_deleted=0`, folderID)
	if err != nil {
		return err
	}
	for objRows.Next() {
		var id int64
		var read int
		if err := objRows.Scan(&id, &read); err != nil {
			objRows.Close()
			return err
		}
		objState[id] = read
	}
	objRows.Close()
	if err := objRows.Err(); err != nil {
		return err
	}

	// Index rows currently in the folder (id -> mid_string).
	idxMid := map[int64]string{}
	idxRows, err := s.idxdb.Query(`SELECT message_id, mid_string FROM messages WHERE folder_id=?`, folderID)
	if err != nil {
		return err
	}
	for idxRows.Next() {
		var id int64
		var mid string
		if err := idxRows.Scan(&id, &mid); err != nil {
			idxRows.Close()
			return err
		}
		idxMid[id] = mid
	}
	idxRows.Close()
	if err := idxRows.Err(); err != nil {
		return err
	}

	// Prune index rows whose object is gone.
	for id, mid := range idxMid {
		if _, ok := objState[id]; ok {
			continue
		}
		if _, err := s.idxdb.Exec(`DELETE FROM messages WHERE message_id=?`, id); err != nil {
			return err
		}
		if _, err := s.idxdb.Exec(`DELETE FROM mapping WHERE message_id=?`, id); err != nil {
			return err
		}
		_ = os.Remove(s.emlPath(mid))
	}

	// Index object messages missing from the index.
	for id, read := range objState {
		if _, ok := idxMid[id]; ok {
			continue
		}
		msg, err := s.OpenMessage(id)
		if err != nil {
			return err
		}
		mid := midString(uint64(id))
		eml, err := oxcmail.Export(msg, oxcmail.Options{Resolver: s.GetNamedPropIDs})
		if err != nil {
			return fmt.Errorf("objectstore: export: %w", err)
		}
		if err := s.writeEML(mid, eml); err != nil {
			return err
		}
		var flags int64
		if read != 0 {
			flags |= FlagSeen
		}
		if _, err := s.indexMessage(folderID, id, mid, msg, int64(len(eml)), deliveryTime(msg.Props), flags); err != nil {
			return err
		}
	}
	return nil
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

package objectstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"time"

	"hermex/internal/mapi"
)

// customEIDBegin is the first store-level object EID handed out on a fresh
// mailbox. The space below it (1..PrivateFIDUnassignedStart-1) is reserved for
// built-in folder ids, and folder message ranges are carved separately above
// AllocatedEIDRange.
const customEIDBegin = 0x100

// sqlExec is satisfied by both *sql.DB and *sql.Tx, so the allocators run
// either standalone or inside a caller's transaction (which provides atomicity
// for a multi-step object creation).
type sqlExec interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// randomGUID returns a cryptographically random 128-bit GUID for the mailbox
// identity.
func randomGUID() (mapi.GUID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return mapi.GUID{}, err
	}
	return mapi.GUID{
		Data1: binary.LittleEndian.Uint32(b[0:4]),
		Data2: binary.LittleEndian.Uint16(b[4:6]),
		Data3: binary.LittleEndian.Uint16(b[6:8]),
		Data4: [8]byte(b[8:16]),
	}, nil
}

// seedStore writes the initial configurations rows and the first EID range for
// a fresh mailbox: a random store GUID and an independent random mapping
// signature, the store-level EID cursor starting at customEIDBegin with a
// maximum of AllocatedEIDRange-1, a zero change-number counter, and the [1, max]
// allocated range. It returns the store GUID, which the caller uses as the
// replica GUID when seeding built-in folders (created separately, carving their
// own ranges above this one).
func (s *Store) seedStore() (mapi.GUID, error) {
	g, err := randomGUID()
	if err != nil {
		return mapi.GUID{}, err
	}
	sig, err := randomGUID()
	if err != nil {
		return mapi.GUID{}, err
	}
	maxEID := int64(mapi.AllocatedEIDRange) - 1

	tx, err := s.objdb.Begin()
	if err != nil {
		return mapi.GUID{}, err
	}
	defer tx.Rollback()

	ins := func(id int, val any) error {
		_, err := tx.Exec(`INSERT INTO configurations (config_id, config_value) VALUES (?, ?)`, id, val)
		return err
	}
	if err := ins(cfgMailboxGUID, g.String()); err != nil {
		return mapi.GUID{}, err
	}
	if err := ins(cfgMappingSignature, sig.String()); err != nil {
		return mapi.GUID{}, err
	}
	for _, kv := range []struct {
		id  int
		val int64
	}{
		{cfgCurrentEID, customEIDBegin},
		{cfgMaximumEID, maxEID},
		{cfgLastChangeNumber, int64(mapi.ChangeNumberBegin)},
		{cfgLastCID, 0},
		{cfgLastArticleNumber, 0},
		{cfgSearchState, 0},
		{cfgDefaultPermission, 0},
		{cfgAnonymousPerm, 0},
	} {
		if err := ins(kv.id, kv.val); err != nil {
			return mapi.GUID{}, err
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO allocated_eids (range_begin, range_end, allocate_time, is_system) VALUES (1, ?, ?, 1)`,
		maxEID, time.Now().Unix()); err != nil {
		return mapi.GUID{}, err
	}
	if err := tx.Commit(); err != nil {
		return mapi.GUID{}, err
	}
	return g, nil
}

// allocateCN returns the next change number, incrementing the stored counter.
func allocateCN(q sqlExec) (uint64, error) {
	var last uint64
	err := q.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgLastChangeNumber).Scan(&last)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	last++
	if _, err := q.Exec(`REPLACE INTO configurations (config_id, config_value) VALUES (?, ?)`, cfgLastChangeNumber, int64(last)); err != nil {
		return 0, err
	}
	return last, nil
}

// allocateArticle returns the next per-folder article number, incrementing the
// stored counter.
func allocateArticle(q sqlExec) (uint64, error) {
	var last uint64
	err := q.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgLastArticleNumber).Scan(&last)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	last++
	if _, err := q.Exec(`REPLACE INTO configurations (config_id, config_value) VALUES (?, ?)`, cfgLastArticleNumber, int64(last)); err != nil {
		return 0, err
	}
	return last, nil
}

// allocateEID hands out the next store-level object EID, carving a fresh
// AllocatedEIDRange-wide range when the current one is exhausted.
func allocateEID(q sqlExec) (uint64, error) {
	var curEID, maxEID uint64
	if err := q.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgCurrentEID).Scan(&curEID); err != nil {
		return 0, err
	}
	if err := q.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgMaximumEID).Scan(&maxEID); err != nil {
		return 0, err
	}
	if curEID >= maxEID {
		var rangeEnd uint64
		if err := q.QueryRow(`SELECT MAX(range_end) FROM allocated_eids`).Scan(&rangeEnd); err != nil {
			return 0, err
		}
		curEID = rangeEnd
		maxEID = curEID + mapi.AllocatedEIDRange
		curEID++
		if _, err := q.Exec(
			`INSERT INTO allocated_eids (range_begin, range_end, allocate_time, is_system) VALUES (?, ?, ?, 1)`,
			curEID+1, maxEID, time.Now().Unix()); err != nil {
			return 0, err
		}
		if _, err := q.Exec(`UPDATE configurations SET config_value=? WHERE config_id=?`, int64(maxEID), cfgMaximumEID); err != nil {
			return 0, err
		}
	}
	eid := curEID
	curEID++
	if _, err := q.Exec(`UPDATE configurations SET config_value=? WHERE config_id=?`, int64(curEID), cfgCurrentEID); err != nil {
		return 0, err
	}
	return eid, nil
}

// allocateLocalIDs reserves count contiguous store EIDs and returns the first. It
// reuses the single-id allocator and verifies contiguity rather than duplicating
// the range-carving: if the block would straddle an allocation-range boundary it
// fails loudly, and the caller retries against the fresh range. With
// AllocatedEIDRange-wide ranges this is rare for the small counts a client reserves.
func allocateLocalIDs(q sqlExec, count uint64) (uint64, error) {
	if count == 0 {
		return 0, fmt.Errorf("objectstore: zero-count id reservation")
	}
	begin, err := allocateEID(q)
	if err != nil {
		return 0, err
	}
	prev := begin
	for i := uint64(1); i < count; i++ {
		next, err := allocateEID(q)
		if err != nil {
			return 0, err
		}
		if next != prev+1 {
			return 0, fmt.Errorf("objectstore: id reservation crossed a range boundary at %d, cannot return %d contiguous ids", next, count)
		}
		prev = next
	}
	return begin, nil
}

// AllocateLocalIDs reserves count contiguous local ids for a client
// (RopGetLocalReplicaIds), returning the first id's value and the home replica
// GUID. A client forms the source keys of new items it uploads from these.
func (s *Store) AllocateLocalIDs(count uint32) (uint64, mapi.GUID, error) {
	home, err := s.replicaGUID()
	if err != nil {
		return 0, mapi.GUID{}, err
	}
	tx, err := s.objdb.Begin()
	if err != nil {
		return 0, mapi.GUID{}, err
	}
	defer tx.Rollback()
	begin, err := allocateLocalIDs(tx, uint64(count))
	if err != nil {
		return 0, mapi.GUID{}, err
	}
	if err := tx.Commit(); err != nil {
		return 0, mapi.GUID{}, err
	}
	return begin, home, nil
}

// allocateEIDFromFolder hands out the next message EID from a folder's own
// reserved range, carving a fresh range when exhausted.
func allocateEIDFromFolder(q sqlExec, folderID int64) (uint64, error) {
	var curEID, maxEID uint64
	if err := q.QueryRow(`SELECT cur_eid, max_eid FROM folders WHERE folder_id=?`, folderID).Scan(&curEID, &maxEID); err != nil {
		return 0, err
	}
	if curEID >= maxEID {
		var rangeEnd uint64
		if err := q.QueryRow(`SELECT MAX(range_end) FROM allocated_eids`).Scan(&rangeEnd); err != nil {
			return 0, err
		}
		curEID = rangeEnd
		maxEID = curEID + mapi.AllocatedEIDRange
		curEID++
		if _, err := q.Exec(
			`INSERT INTO allocated_eids (range_begin, range_end, allocate_time, is_system) VALUES (?, ?, ?, 1)`,
			curEID, maxEID, time.Now().Unix()); err != nil {
			return 0, err
		}
	}
	eid := curEID
	curEID++
	if _, err := q.Exec(`UPDATE folders SET cur_eid=?, max_eid=? WHERE folder_id=?`, int64(curEID), int64(maxEID), folderID); err != nil {
		return 0, err
	}
	return eid, nil
}

// allocateRange reserves a fresh AllocatedEIDRange-wide message range for a new
// folder, returning [begin, end]. It mirrors how a built-in folder's initial
// cur_eid/max_eid are carved from the EID space.
func allocateRange(q sqlExec) (begin, end uint64, err error) {
	var rangeEnd uint64
	if err = q.QueryRow(`SELECT MAX(range_end) FROM allocated_eids`).Scan(&rangeEnd); err != nil {
		return 0, 0, err
	}
	begin = rangeEnd + 1
	end = begin + mapi.AllocatedEIDRange - 1
	if _, err = q.Exec(
		`INSERT INTO allocated_eids (range_begin, range_end, allocate_time, is_system) VALUES (?, ?, ?, 1)`,
		int64(begin), int64(end), time.Now().Unix()); err != nil {
		return 0, 0, err
	}
	return begin, end, nil
}

// storeGUID returns the mailbox GUID recorded at creation.
func (s *Store) storeGUID() (string, error) {
	var g string
	err := s.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgMailboxGUID).Scan(&g)
	if err != nil {
		return "", fmt.Errorf("objectstore: read mailbox guid: %w", err)
	}
	return g, nil
}

// StoreGUID returns the mailbox's store GUID (its PR_STORE_RECORD_KEY identity),
// recorded at creation. It is the same value stamped as the replica GUID into
// folder change keys, so a logon advertising it stays consistent with the entry
// ids the store hands out.
func (s *Store) StoreGUID() (mapi.GUID, error) { return s.replicaGUID() }

// MappingSignature returns the mailbox's mapping-signature GUID
// (PR_MAPPING_SIGNATURE), recorded at creation. A private-mailbox logon reports
// it as the replica GUID for the per-MDB replid mapping.
func (s *Store) MappingSignature() (mapi.GUID, error) {
	var str string
	if err := s.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgMappingSignature).Scan(&str); err != nil {
		return mapi.GUID{}, fmt.Errorf("objectstore: read mapping signature: %w", err)
	}
	return mapi.ParseGUID(str)
}

package objectstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
	"hermex/internal/oxvcard"
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
//
// With create false the resolver only reads, so it runs without a transaction.
// The connection takes its write lock at BEGIN, so wrapping this read would make
// every caller queue behind any concurrent writer for the busy timeout, and this
// is one of the most called methods in the store.
func (s *Store) GetNamedPropIDs(create bool, names []mapi.PropertyName) ([]uint16, error) {
	if !create {
		return getNamedPropIDs(s.objdb, false, names)
	}
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
	s.recordDeclinedHeaderNames(names, ids)
	return ids, nil
}

// recordDeclinedHeaderNames logs, once per allocation, how many PS_INTERNET_HEADERS
// names were left unresolved, so a store that reached its quota shows why received
// fields stopped reaching clients.
func (s *Store) recordDeclinedHeaderNames(names []mapi.PropertyName, ids []uint16) {
	declined := 0
	for i, n := range names {
		if ids[i] == 0 && n.GUID == mapi.PsInternetHeaders {
			declined++
		}
	}
	if declined > 0 {
		s.LogSwallowedError("objectstore.internet_header_name", fmt.Errorf("%w: %d declined", errInternetHeaderQuota, declined))
	}
}

// ExportOptions are the oxcmail options that render a message of this store: its
// named properties resolve both ways, so an opaque S/MIME message finds its stored
// Content-Type and every PS_INTERNET_HEADERS property becomes a header field, and
// an attached contact goes out as a vCard.
func (s *Store) ExportOptions() oxcmail.Options {
	return oxcmail.Options{Resolver: s.GetNamedPropIDs, PropName: s.NamedPropName, ContactCard: oxvcard.EmbeddedCard}
}

// NamedPropName resolves a store property id back to its PropertyName, the
// reverse of GetNamedPropIDs. The download path needs it to emit a named
// property's GUID/kind/LID-or-name inline in the FastTransfer stream so the
// receiver can remap it to its own local id. ok is false for an id with no
// mapping (a static property id below namedPropBase, or an unknown one).
func (s *Store) NamedPropName(propid uint16) (mapi.PropertyName, bool, error) {
	var key string
	err := s.objdb.QueryRow(`SELECT name_string FROM named_properties WHERE propid=?`, int64(propid)).Scan(&key)
	if err == sql.ErrNoRows {
		return mapi.PropertyName{}, false, nil
	}
	if err != nil {
		return mapi.PropertyName{}, false, err
	}
	return parseNamedPropKey(key)
}

// parseNamedPropKey reverses namedPropKey. The GUID prints without a comma, so
// the first comma after the "GUID=" prefix splits the namespace from the
// "LID=<n>" or "NAME=<...>" tail; a name may itself contain commas and is taken
// verbatim after "NAME=".
func parseNamedPropKey(key string) (mapi.PropertyName, bool, error) {
	rest, ok := strings.CutPrefix(key, "GUID=")
	if !ok {
		return mapi.PropertyName{}, false, fmt.Errorf("objectstore: malformed named-prop key %q", key)
	}
	guidStr, tail, found := strings.Cut(rest, ",")
	if !found {
		return mapi.PropertyName{}, false, fmt.Errorf("objectstore: malformed named-prop key %q", key)
	}
	guid, err := mapi.ParseGUID(guidStr)
	if err != nil {
		return mapi.PropertyName{}, false, fmt.Errorf("objectstore: named-prop key %q: %w", key, err)
	}
	if lid, ok := strings.CutPrefix(tail, "LID="); ok {
		n, err := strconv.ParseUint(lid, 10, 32)
		if err != nil {
			return mapi.PropertyName{}, false, fmt.Errorf("objectstore: named-prop key %q: %w", key, err)
		}
		return mapi.PropertyName{Kind: mapi.MnidID, GUID: guid, LID: uint32(n)}, true, nil
	}
	if name, ok := strings.CutPrefix(tail, "NAME="); ok {
		return mapi.PropertyName{Kind: mapi.MnidString, GUID: guid, Name: name}, true, nil
	}
	return mapi.PropertyName{}, false, fmt.Errorf("objectstore: malformed named-prop key %q", key)
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
		id, err := allocateNamedProp(q, n, key)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}

// internetHeaderNameQuota bounds how many PS_INTERNET_HEADERS names a store holds.
// Every received header field no other property maps becomes one of these names,
// and a sender chooses the field names, so without a bound a stream of mail with
// invented X- fields would spend the whole named-property id space and every
// later allocation, including the ones delivery needs, would fail.
const internetHeaderNameQuota = 4000

// errInternetHeaderQuota names why a PS_INTERNET_HEADERS name was left unresolved:
// its quota is spent, or the name is too long to store.
var errInternetHeaderQuota = errors.New("objectstore: PS_INTERNET_HEADERS name quota reached")

// allocateNamedProp assigns the next id to a name not seen before, computing it
// explicitly so the floor is namedPropBase even on an empty table (no pre-seed
// needed). A PS_INTERNET_HEADERS name past its quota is declined with id 0.
func allocateNamedProp(q sqlExec, n mapi.PropertyName, key string) (uint16, error) {
	if n.GUID == mapi.PsInternetHeaders {
		var held int
		prefix := "GUID=" + mapi.PsInternetHeaders.String() + ",%"
		if err := q.QueryRow(`SELECT COUNT(*) FROM named_properties WHERE name_string LIKE ?`, prefix).Scan(&held); err != nil {
			return 0, err
		}
		if held >= internetHeaderNameQuota {
			return 0, nil
		}
	}
	var maxID uint64
	if err := q.QueryRow(`SELECT COALESCE(MAX(propid), ?) FROM named_properties`, int64(namedPropBase-1)).Scan(&maxID); err != nil {
		return 0, err
	}
	next := maxID + 1
	if next > namedPropMax {
		return 0, fmt.Errorf("objectstore: named-property id space exhausted")
	}
	if _, err := q.Exec(`INSERT INTO named_properties (propid, name_string) VALUES (?, ?)`, int64(next), key); err != nil {
		return 0, err
	}
	return uint16(next), nil
}

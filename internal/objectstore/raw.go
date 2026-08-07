package objectstore

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sync/singleflight"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// regenGroup collapses concurrent regenerations of one cached message into a
// single pass. The key is the cache path, so it identifies the message rather than
// the handle asking for it: a mailbox is opened per request, so two readers of the
// same message hold different *Store values over the same directory and a lock on
// either one would collapse nothing.
//
// This is not only about the wasted work. Regeneration mints fresh MIME
// boundaries, so two passes over one message produce different bytes of possibly
// different length, and each pass writes the file and then records the length.
// Interleave the two and the file is one pass's bytes while the recorded size is
// the other's, which breaks the invariant this whole path exists to hold: that
// RFC822.SIZE equals the bytes served. A client that fetches by that size then
// reads the wrong number of bytes.
//
// It is per-process. Two daemons regenerating the same message at the same instant
// still race; collapsing that would need the export to be deterministic rather
// than a lock.
var regenGroup singleflight.Group

// GetMessageRaw returns the RFC822 wire form of a message by folder and IMAP
// UID. It serves the cached eml; on a cache miss it re-synthesizes the wire
// form from the stored object, caches it, and updates the index size so
// RFC822.SIZE always equals the bytes served. It reports ErrNotFound when no
// such message exists.
func (s *Store) GetMessageRaw(folderID int64, uid uint32) (raw []byte, err error) {
	// A read failure during serving (a FETCH, a RETR, a webmail render) is
	// otherwise invisible — the protocol layer only logs the request, not the
	// store error underneath it. Report any infrastructure failure here; a
	// benign ErrNotFound is not one.
	defer func() {
		if err != nil && !errors.Is(err, ErrNotFound) {
			s.logStoreError("read", err)
		}
	}()
	var messageID int64
	var mid string
	err = s.idxdb.QueryRow(
		`SELECT message_id, mid_string FROM messages WHERE folder_id=? AND uid=?`,
		folderID, int64(uid)).Scan(&messageID, &mid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	eml, err := os.ReadFile(s.emlPath(mid))
	if err == nil {
		return eml, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return s.regenerateOnce(messageID, mid)
}

// regenerateOnce runs regenerateEML under the shared flight for this message, so
// concurrent readers of one cache miss produce one regeneration and all see the
// same bytes. The result is copied per caller: singleflight hands every waiter the
// same slice, and the cache-hit path above gives each caller its own.
func (s *Store) regenerateOnce(messageID int64, mid string) ([]byte, error) {
	v, err, _ := regenGroup.Do(s.emlPath(mid), func() (any, error) {
		return s.regenerateEML(messageID, mid)
	})
	if err != nil {
		return nil, err
	}
	return bytes.Clone(v.([]byte)), nil
}

// regenerateEML re-synthesizes a message's wire form from the stored object,
// writes it to the eml cache, and updates the index size so the recorded
// RFC822.SIZE matches the bytes now served (a regenerated message uses fresh
// MIME boundaries and may differ in length from any earlier rendering).
func (s *Store) regenerateEML(messageID int64, mid string) ([]byte, error) {
	// A preserved original — an S/MIME message, or a scheduling message whose
	// text/calendar body re-export would demote to an attachment — is served
	// verbatim: re-synthesizing it would destroy the signature, the envelope, or the
	// invitation, so it is never regenerated via Export.
	for _, tag := range []mapi.PropTag{mapi.PrSmimeOriginal, mapi.PrScheduleOriginal} {
		props, err := s.GetMessageProperties(messageID, tag)
		if err != nil {
			continue
		}
		if v, ok := props.Get(tag); ok {
			if orig, ok := v.([]byte); ok && len(orig) > 0 {
				if err := s.writeEML(mid, orig); err != nil {
					return nil, err
				}
				return orig, nil
			}
		}
	}
	msg, err := s.OpenMessage(messageID)
	if err != nil {
		return nil, err
	}
	eml, err := oxcmail.Export(msg, oxcmail.Options{Resolver: s.GetNamedPropIDs})
	if err != nil {
		return nil, fmt.Errorf("objectstore: export: %w", err)
	}
	if err := s.writeEML(mid, eml); err != nil {
		return nil, err
	}
	if _, err := s.idxdb.Exec(`UPDATE messages SET size=? WHERE message_id=?`, int64(len(eml)), messageID); err != nil {
		return nil, err
	}
	return eml, nil
}

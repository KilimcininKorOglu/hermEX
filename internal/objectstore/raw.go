package objectstore

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// GetMessageRaw returns the RFC822 wire form of a message by folder and IMAP
// UID. It serves the cached eml; on a cache miss it re-synthesizes the wire
// form from the stored object, caches it, and updates the index size so
// RFC822.SIZE always equals the bytes served. It reports ErrNotFound when no
// such message exists.
func (s *Store) GetMessageRaw(folderID int64, uid uint32) ([]byte, error) {
	var messageID int64
	var mid string
	err := s.idxdb.QueryRow(
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
	return s.regenerateEML(messageID, mid)
}

// regenerateEML re-synthesizes a message's wire form from the stored object,
// writes it to the eml cache, and updates the index size so the recorded
// RFC822.SIZE matches the bytes now served (a regenerated message uses fresh
// MIME boundaries and may differ in length from any earlier rendering).
func (s *Store) regenerateEML(messageID int64, mid string) ([]byte, error) {
	// A preserved S/MIME original is served verbatim: re-synthesizing it would
	// destroy the signature or envelope, so it is never regenerated via Export.
	if props, err := s.GetMessageProperties(messageID, mapi.PrSmimeOriginal); err == nil {
		if v, ok := props.Get(mapi.PrSmimeOriginal); ok {
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

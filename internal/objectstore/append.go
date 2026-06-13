package objectstore

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// MessageInfo is the per-message metadata IMAP and POP3 need without loading
// the full message body.
type MessageInfo struct {
	ID           int64
	UID          uint32
	InternalDate time.Time
	Size         int64
	Flags        int64
	// Subject and Sender are the index's denormalized envelope projections,
	// carried so a folder listing needs no per-message wire-form read. Sender is
	// the formatted originator ("Name <addr>"); see projectSubject/projectSender.
	Subject string
	Sender  string
}

// AppendMessage stores a raw RFC822 message in a folder as a MAPI object: it
// imports the message into the object model, persists the object, re-synthesizes
// the wire form and caches it as the served eml, then indexes the message for
// IMAP/POP3. The original bytes are not retained — the served form is
// regenerated from the object, so it is well-formed but not byte-identical to
// arrival. The eml is generated now (rather than lazily) so the index records
// the exact RFC822 size IMAP reports for the message. It returns the message's
// index metadata, including its allocated UID.
func (s *Store) AppendMessage(folderID int64, raw []byte, internalDate time.Time, flags int64) (MessageInfo, error) {
	resolver := oxcmail.Options{Resolver: s.GetNamedPropIDs}

	msg, err := oxcmail.Import(raw, resolver)
	if err != nil {
		return MessageInfo{}, fmt.Errorf("objectstore: import: %w", err)
	}
	// Delivery stamps the delivery time when the message does not carry one.
	if !msg.Props.Has(mapi.PrMessageDeliveryTime) {
		msg.Props.Set(mapi.PrMessageDeliveryTime, mapi.UnixToNTTime(internalDate))
	}

	eid, err := s.CreateMessage(folderID, msg)
	if err != nil {
		return MessageInfo{}, err
	}
	mid := midString(uint64(eid))

	eml, err := oxcmail.Export(msg, resolver)
	if err != nil {
		return MessageInfo{}, fmt.Errorf("objectstore: export: %w", err)
	}
	if err := s.writeEML(mid, eml); err != nil {
		return MessageInfo{}, err
	}

	uid, err := s.indexMessage(folderID, eid, mid, msg, int64(len(eml)), internalDate, flags)
	if err != nil {
		return MessageInfo{}, err
	}
	return MessageInfo{
		ID:           eid,
		UID:          uint32(uid),
		InternalDate: internalDate.UTC(),
		Size:         int64(len(eml)),
		Flags:        flags,
		Subject:      projectSubject(msg.Props),
		Sender:       projectSender(msg.Props),
	}, nil
}

// emlPath maps a message's mid_string to its cached wire-form file.
func (s *Store) emlPath(mid string) string {
	return filepath.Join(s.dir, "eml", mid)
}

// writeEML writes the re-synthesized wire form to the message's eml cache,
// atomically (temp file + rename) so a reader never sees a partial file.
func (s *Store) writeEML(mid string, data []byte) error {
	path := s.emlPath(mid)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".eml-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

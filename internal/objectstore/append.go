package objectstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/smime"
)

// isSchedulingMessage reports whether a message is an iTIP scheduling message — a
// meeting request, response, or cancellation, by its IPM.Schedule message class.
// Such a message carries its invitation as a text/calendar body that re-export
// would demote to an attachment, so the store preserves it verbatim.
func isSchedulingMessage(msg *oxcmail.Message) bool {
	v, _ := msg.Props.Get(mapi.PrMessageClass)
	class, _ := v.(string)
	return strings.HasPrefix(class, "IPM.Schedule")
}

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
func (s *Store) AppendMessage(folderID int64, raw []byte, internalDate time.Time, flags int64) (info MessageInfo, err error) {
	// Every error exit here is a real store failure (a conversion, SQL, or IO
	// error) — there is no benign not-found path on the write side — so report
	// any of them under the store subsystem.
	defer func() {
		if err != nil {
			s.logStoreError("append", err)
		}
	}()
	resolver := oxcmail.Options{
		Resolver: s.GetNamedPropIDs,
		// A delivered meeting request/response carries its appointment as a
		// text/calendar part; bridge to the iCalendar converter (oxcmail cannot
		// import it directly without a cycle) so Import overlays the scheduling
		// class and appointment properties onto the stored message.
		CalendarImporter: func(ical []byte) (mapi.PropertyValues, error) {
			m, err := oxcical.Import(ical, oxcical.Options{Resolver: s.GetNamedPropIDs})
			if err != nil {
				return nil, err
			}
			return m.Props, nil
		},
	}

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

	// Some messages must be served byte-for-byte rather than re-synthesized:
	// oxcmail.Export rebuilds the MIME tree, which invalidates an S/MIME signature
	// (turning an envelope into plain multipart/mixed) and demotes a meeting
	// invitation's text/calendar body to an attachment. For those, preserve the
	// arrival bytes on the message and serve them verbatim; every other message is
	// re-synthesized.
	eml := raw
	switch {
	case smime.IsSMIME(raw):
		if err := s.SetMessageProperties(eid, mapi.PropertyValues{
			{Tag: mapi.PrSmimeOriginal, Value: raw},
		}); err != nil {
			return MessageInfo{}, err
		}
	case isSchedulingMessage(msg):
		if err := s.SetMessageProperties(eid, mapi.PropertyValues{
			{Tag: mapi.PrScheduleOriginal, Value: raw},
		}); err != nil {
			return MessageInfo{}, err
		}
	default:
		eml, err = oxcmail.Export(msg, resolver)
		if err != nil {
			return MessageInfo{}, fmt.Errorf("objectstore: export: %w", err)
		}
	}
	if err := s.writeEML(mid, eml); err != nil {
		return MessageInfo{}, err
	}

	uid, err := s.indexMessage(folderID, eid, mid, msg, int64(len(eml)), internalDate, flags)
	if err != nil {
		return MessageInfo{}, err
	}
	// Re-publish now the IMAP index row exists. CreateMessage already published a
	// "create" for the object store (which MAPI/EWS diff), but the message is not
	// visible to IMAP/EAS until indexed here, so a consumer woken by that earlier
	// event would not yet see it. This second wake fires once the index is in place.
	s.publishChange("create", 0, mid)
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

// removeEML deletes a message's cached wire form. The eml is a regenerable cache,
// so a delete never fails the caller; an already-absent file is normal and
// silent, but a real filesystem error (a permission or I/O fault that would
// otherwise orphan the file undetected) is logged so it is diagnosable rather
// than swallowed.
func (s *Store) removeEML(mid string) {
	if err := os.Remove(s.emlPath(mid)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.logStoreError("remove-eml", err)
	}
}

// refreshEML rebuilds a message's cached wire form after an in-place edit, so the
// bytes GetMessageRaw serves and the RFC822 size the index reports both follow the
// edit. Without it the cache is invalidated only by deletion, and an edited message
// keeps serving its pre-edit bytes to every daemon that opens the mailbox.
//
// A message that has no cache is left alone: a non-mail object (a contact, a calendar
// item) never has one, and an edit must not manufacture one. A rebuild failure drops
// the stale file rather than keeping it, since serving pre-edit bytes is the very
// fault this closes; the next read re-synthesizes and surfaces the real error. The
// cache is regenerable, so this never fails the edit that triggered it.
func (s *Store) refreshEML(messageID int64) {
	mid := midString(uint64(messageID))
	if _, err := os.Stat(s.emlPath(mid)); err != nil {
		return
	}
	if _, err := s.regenerateEML(messageID, mid); err != nil {
		s.removeEML(mid)
		s.logStoreError("refresh-eml", err)
	}
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

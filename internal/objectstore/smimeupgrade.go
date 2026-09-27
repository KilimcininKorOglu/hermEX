package objectstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// smimeUpgradedMarker records that every S/MIME message in the mailbox is in the
// [MS-OXOSMIME] shape, so the pass that finds the older ones runs once.
const smimeUpgradedMarker = ".smime-shape"

// upgradeSMIMEMessages rewrites each S/MIME message delivered before the import
// gave it the [MS-OXOSMIME] shape: stored as a plain note with its signature as a
// loose attachment, which Outlook shows as unsigned mail and cannot decrypt. The
// preserved arrival bytes are imported again and the message takes the new class
// and attachment in place, so its id, UID and served bytes stay the same.
//
// Finding the old messages reads every property row once, so the pass records a
// marker when every message converted and does not run again. A message that
// cannot be converted is recorded and the marker withheld, so the next open
// retries it, and a pass that finds the mailbox open elsewhere stops and leaves
// the rest to a later open. The mailbox opens either way.
func (s *Store) upgradeSMIMEMessages() {
	marker := filepath.Join(s.dir, smimeUpgradedMarker)
	if _, err := os.Stat(marker); err == nil {
		return
	}
	ids, err := s.smimeCandidates()
	if err != nil {
		s.logStoreError("upgrade_smime", err)
		return
	}
	var failed bool
	for _, id := range ids {
		// Each message converts while this store is the mailbox's only opener, so two
		// processes opening it at once never both replace its attachments. The lock is
		// taken per message and returned in between, so an open elsewhere waits one
		// conversion at most, and a mailbox busy elsewhere is converted on a later open.
		err := s.withExclusiveLock(func() error { return s.upgradeSMIMEMessage(id) })
		if errors.Is(err, ErrMailboxBusy) {
			return
		}
		if err != nil {
			s.logStoreError("upgrade_smime_message", err)
			failed = true
		}
	}
	if failed {
		return
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		s.logStoreError("upgrade_smime_marker", err)
	}
}

// smimeCandidates returns every message that carries preserved S/MIME arrival
// bytes.
func (s *Store) smimeCandidates() ([]int64, error) {
	rows, err := s.objdb.Query(`SELECT message_id FROM message_properties WHERE proptag = ?`,
		int64(uint32(mapi.PrSmimeOriginal)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// errNoSMIMEOriginal reports a candidate whose preserved bytes cannot be read.
var errNoSMIMEOriginal = errors.New("objectstore: S/MIME message lost its preserved bytes")

// upgradeSMIMEMessage converts one message that is still a plain note. The
// attachments are replaced before the class is written, so an interrupted pass
// leaves the old class and the next open converts the message again.
func (s *Store) upgradeSMIMEMessage(id int64) error {
	pv, err := s.GetMessageProperties(id, mapi.PrMessageClass, mapi.PrSmimeOriginal)
	if err != nil {
		return err
	}
	if class, _ := pv.Get(mapi.PrMessageClass); isSMIMEClass(class) {
		return nil
	}
	v, _ := pv.Get(mapi.PrSmimeOriginal)
	raw, ok := v.([]byte)
	if !ok || len(raw) == 0 {
		return errNoSMIMEOriginal
	}
	msg, err := oxcmail.Import(raw, oxcmail.Options{Resolver: s.GetNamedPropIDs})
	if err != nil {
		return err
	}
	class, _ := msg.Props.Get(mapi.PrMessageClass)
	if !isSMIMEClass(class) {
		return nil
	}
	if err := s.replaceAttachments(id, msg.Attachments); err != nil {
		return err
	}
	return s.ModifyMessageProperties(id, smimeProps(msg.Props))
}

// isSMIMEClass reports whether a stored class is one of the S/MIME classes.
func isSMIMEClass(v any) bool {
	class, _ := v.(string)
	class = strings.ToLower(class)
	return strings.HasSuffix(class, ".smime") || strings.HasSuffix(class, ".smime.multipartsigned")
}

// smimeProps is what the conversion writes on the message: the class, and the
// stored Content-Type of an opaque message, a named property.
func smimeProps(props mapi.PropertyValues) mapi.PropertyValues {
	out := mapi.PropertyValues{}
	for _, p := range props {
		if p.Tag == mapi.PrMessageClass || p.Tag.ID() >= 0x8000 {
			out = append(out, p)
		}
	}
	return out
}

// replaceAttachments deletes a message's attachments and stores atts in their
// place.
func (s *Store) replaceAttachments(id int64, atts []oxcmail.Attachment) error {
	old, err := s.OpenMessage(id)
	if err != nil {
		return err
	}
	for _, a := range old.Attachments {
		num, ok := a.Props.Get(mapi.PrAttachNum)
		n, isNum := num.(int32)
		if !ok || !isNum {
			continue
		}
		// #nosec G115 -- an attach number is a per-message counter stored from a uint32
		if err := s.DeleteAttachment(id, uint32(n)); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	for _, a := range atts {
		attID, _, err := s.CreateAttachment(id, nil)
		if err != nil {
			return err
		}
		if err := s.SetAttachmentProperties(attID, a.Props); err != nil {
			return err
		}
	}
	return nil
}

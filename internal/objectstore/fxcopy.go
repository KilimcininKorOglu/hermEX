package objectstore

import (
	"database/sql"
	"errors"
	"fmt"

	"hermex/internal/ics"
	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// This file holds the property-serialization primitives shared by the ICS
// download path (icsdownload.go) and the generic-copy FastTransfer source below,
// plus the generic-copy source itself. Both paths emit properties through the same
// free functions so the two encoders cannot drift in the bytes they produce: the
// ICS download inherits its wire encoding from the same code the generic copy
// uses, and vice versa.

// fxFilter applies a property filter to a bag. includeMode keeps only the listed
// tags; otherwise it drops them. An empty set keeps everything (the no-filter
// default the ICS download relies on, and CopyTo's "no exclusions" case).
func fxFilter(props mapi.PropertyValues, proptags map[mapi.PropTag]struct{}, includeMode bool) mapi.PropertyValues {
	if len(proptags) == 0 {
		return props
	}
	out := make(mapi.PropertyValues, 0, len(props))
	for _, p := range props {
		_, listed := proptags[p.Tag]
		if listed == includeMode {
			out = append(out, p)
		}
	}
	return out
}

// fxToStreamProp maps a stored property to a stream property, resolving a
// named-property id (>= namedPropBase) to the GUID/kind/name the stream carries
// inline so the receiver can remap it. A named id with no mapping is an error
// rather than a silent drop.
func fxToStreamProp(store *Store, p mapi.TaggedPropVal) (ics.StreamProp, error) {
	sp := ics.StreamProp{Tag: p.Tag, Value: p.Value}
	if propid := uint16(uint32(p.Tag) >> 16); uint64(propid) >= namedPropBase {
		name, ok, err := store.NamedPropName(propid)
		if err != nil {
			return sp, err
		}
		if !ok {
			return sp, fmt.Errorf("objectstore: unresolved named property id %#x", propid)
		}
		sp.Name = &name
	}
	return sp, nil
}

// fxWriteProp writes one already-built stream property through the producer.
func fxWriteProp(producer *ics.Producer, sp ics.StreamProp) error {
	if err := producer.WriteProp(sp); err != nil {
		return fmt.Errorf("objectstore: write %s to fast-transfer stream: %w", sp.Tag, err)
	}
	return nil
}

// fxWriteProps resolves a stored property bag to stream properties and writes them.
func fxWriteProps(producer *ics.Producer, store *Store, props mapi.PropertyValues) error {
	for _, p := range props {
		sp, err := fxToStreamProp(store, p)
		if err != nil {
			return err
		}
		if err := fxWriteProp(producer, sp); err != nil {
			return err
		}
	}
	return nil
}

// fxWritePropsBuilt writes already-built stream properties (the serialized state
// meta-tags) through the producer.
func fxWritePropsBuilt(producer *ics.Producer, props []ics.StreamProp) error {
	for _, p := range props {
		if err := fxWriteProp(producer, p); err != nil {
			return err
		}
	}
	return nil
}

// CopyContext is a generic-copy FastTransfer source ([MS-OXCFXICS] 2.2.4): a fully
// rendered stream the client drains chunk by chunk through
// RopFastTransferSourceGetBuffer. Unlike the ICS download it carries no
// synchronization framing (no INCRSYNCCHG, no change header, no state) — the stream
// is a bare messageContent or propList.
//
// v1 scope: the source is a single stored message (CopyTo / CopyProperties).
// Folder and attachment sources, the messageList of CopyMessages, the
// folderContent of CopyFolder, and embedded-message recursion (the CopyTo "level"
// depth) are later slices.
type CopyContext struct {
	producer *ics.Producer
}

// GetBuffer serves up to maxLen bytes of the rendered copy stream; last reports the
// stream is fully drained. Its signature matches the ICS download context so both
// satisfy the dispatch layer's FastTransfer source.
func (c *CopyContext) GetBuffer(maxLen int) (chunk []byte, last bool, err error) {
	chunk, drained := c.producer.ReadBuffer(maxLen)
	return chunk, drained, nil
}

// NewCopyToMessageSource renders a stored message as a generic-copy messageContent
// ([MS-OXCFXICS] 2.2.4.1.2): its filtered property list followed by its recipient
// and attachment lists. exclude lists the property tags to omit (CopyTo's exclusion
// set); an empty set copies every property.
func (s *Store) NewCopyToMessageSource(messageID int64, exclude []mapi.PropTag) (*CopyContext, error) {
	msg, err := s.OpenMessage(messageID)
	if err != nil {
		return nil, err
	}
	pr := &ics.Producer{}
	if err := writeCopyMessageContent(pr, s, msg, propTagSet(exclude), false); err != nil {
		return nil, err
	}
	return &CopyContext{producer: pr}, nil
}

// NewCopyPropertiesMessageSource renders a stored message's property list only —
// the CopyProperties body, with no recipients or attachments ([MS-OXCFXICS]
// 2.2.4.1.1). include is the inclusive tag set; an empty set copies nothing.
func (s *Store) NewCopyPropertiesMessageSource(messageID int64, include []mapi.PropTag) (*CopyContext, error) {
	msg, err := s.OpenMessage(messageID)
	if err != nil {
		return nil, err
	}
	pr := &ics.Producer{}
	// An empty inclusive set selects no properties. fxFilter's empty-set rule keeps
	// everything (the exclusion default), so the empty CopyProperties case is guarded
	// here to emit nothing instead.
	if len(include) > 0 {
		if err := fxWriteProps(pr, s, fxFilter(msg.Props, propTagSet(include), true)); err != nil {
			return nil, err
		}
	}
	return &CopyContext{producer: pr}, nil
}

// NewCopyMessagesSource renders the listed messages of a folder as a generic-copy
// messageList ([MS-OXCFXICS] 2.2.4.1.3): each message framed by a StartMessage (or
// StartFAIMsg for an associated message) marker, its messageContent, and an
// EndMessage marker. The message ids are supplied by the client (the CopyMessages
// request carries them); each must be a live message of folderID. exclude is the
// property-tag exclusion set applied to every message; an empty set copies all.
func (s *Store) NewCopyMessagesSource(folderID int64, messageIDs []int64, exclude []mapi.PropTag) (*CopyContext, error) {
	pr := &ics.Producer{}
	proptags := propTagSet(exclude)
	for _, mid := range messageIDs {
		fai, err := s.messageIsAssociated(folderID, mid)
		if err != nil {
			return nil, err
		}
		msg, err := s.OpenMessage(mid)
		if err != nil {
			return nil, err
		}
		if fai {
			pr.WriteMarker(ics.MarkerStartFAIMsg)
		} else {
			pr.WriteMarker(ics.MarkerStartMessage)
		}
		if err := writeCopyMessageContent(pr, s, msg, proptags, false); err != nil {
			return nil, err
		}
		pr.WriteMarker(ics.MarkerEndMessage)
	}
	return &CopyContext{producer: pr}, nil
}

// messageIsAssociated reports whether a live message of folderID is an associated
// (FAI) message, selecting the StartFAIMsg vs StartMessage marker for the
// messageList. A message id that is not a live row of folderID is ErrNotFound — a
// CopyMessages source emits only messages of its own folder.
func (s *Store) messageIsAssociated(folderID, messageID int64) (bool, error) {
	var assoc int
	err := s.objdb.QueryRow(
		`SELECT is_associated FROM messages WHERE message_id=? AND parent_fid=? AND is_deleted=0`,
		messageID, folderID).Scan(&assoc)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	return assoc != 0, nil
}

// writeCopyMessageContent emits a generic-copy messageContent: the filtered property
// list, then the recipient list, then the attachment list. Each sub-object list is
// prefixed by a MetaTagFXDelProp for its collection so the destination starts from
// an empty collection and replaces rather than merges ([MS-OXCFXICS] 2.2.4.1.2). It
// mirrors the ICS message-change body (writeMessageChange) minus the change header
// and the INCRSYNCMESSAGE marker — generic-copy messageContent begins directly with
// the property list.
func writeCopyMessageContent(pr *ics.Producer, store *Store, msg *oxcmail.Message, proptags map[mapi.PropTag]struct{}, includeMode bool) error {
	if err := fxWriteProps(pr, store, fxFilter(msg.Props, proptags, includeMode)); err != nil {
		return err
	}
	if err := fxWriteProp(pr, ics.StreamProp{Tag: mapi.PropTag(ics.MetaTagFXDelProp), Value: int32(mapi.PrMessageRecipients)}); err != nil {
		return err
	}
	for _, r := range msg.Recipients {
		pr.WriteMarker(ics.MarkerStartRecip)
		if err := fxWriteProps(pr, store, fxFilter(r, proptags, includeMode)); err != nil {
			return err
		}
		pr.WriteMarker(ics.MarkerEndToRecip)
	}
	if err := fxWriteProp(pr, ics.StreamProp{Tag: mapi.PropTag(ics.MetaTagFXDelProp), Value: int32(mapi.PrMessageAttachments)}); err != nil {
		return err
	}
	for _, a := range msg.Attachments {
		pr.WriteMarker(ics.MarkerNewAttach)
		if err := fxWriteProps(pr, store, fxFilter(a.Props, proptags, includeMode)); err != nil {
			return err
		}
		pr.WriteMarker(ics.MarkerEndAttach)
	}
	return nil
}

// propTagSet builds a lookup set from a property-tag list; nil for an empty list.
func propTagSet(tags []mapi.PropTag) map[mapi.PropTag]struct{} {
	if len(tags) == 0 {
		return nil
	}
	m := make(map[mapi.PropTag]struct{}, len(tags))
	for _, t := range tags {
		m[t] = struct{}{}
	}
	return m
}

package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// OpenEmbeddedMessage open-mode flags ([MS-OXCMSG] 2.2.3.22.1, mapidefs MAPI_*):
// MAPI_MODIFY opens for write, MAPI_CREATE permits creating a new embedded message
// when the attachment holds none.
const (
	mapiModify uint8 = 0x01
	mapiCreate uint8 = 0x02
)

// ropOpenEmbeddedMessage handles RopOpenEmbeddedMessage ([MS-OXCMSG] 2.2.3.22): it
// opens the message encapsulated in the parent attachment. The embedded message is
// addressed only by the parent attachment handle — the request carries no message
// id. hermEX stores an embedded message as the raw RFC822 bytes in the parent
// attachment's PR_ATTACH_DATA_BIN (the proven oxcmail path), so the handler serves
// reads by importing those bytes and serves a compose (MAPI_CREATE) by exporting
// the message back into the attachment on save.
//
// Two parents are handled. An opened attachment (kindAttachment) that carries a
// method-5 encapsulated message is imported for reading; with no such message it
// reports MAPI_E_NOT_FOUND, matching the reference's no-auto-create contract. A
// freshly created attachment (kindAttachWrite) with MAPI_CREATE opens an empty
// embedded message to compose; SaveChangesMessage exports it back into that
// attachment's pending bag (see ropSaveChangesMessage).
//
// The response carries a Reserved byte and a MessageId ahead of the same tail as
// RopOpenMessage. With no store row the MessageId is synthesized from the object
// handle: the embedded message is re-addressed by handle, so the value need only be
// stable and non-zero for this open.
func (s *Session) ropOpenEmbeddedMessage(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8() // OutputHandleIndex
	_, e2 := p.Uint16()      // Cpid
	flags, e3 := p.Uint8()   // OpenEmbeddedMessageFlags (MAPI_MODIFY / MAPI_CREATE)
	if e1 != nil || e2 != nil || e3 != nil {
		return false
	}
	att := s.get(handleAt(handles, hindex))
	if att == nil {
		writeErr(out, ropOpenEmbeddedMessage, ohindex, ecNotSupported)
		return true
	}

	switch att.kind {
	case kindAttachment:
		// Open an existing embedded message: import the encapsulated bytes. The
		// embedded message lives behind a method-5 (afEmbeddedMessage) attachment.
		// Anything else has no embedded message to open; creating one in place over an
		// opened attachment is not supported in v1 (the compose path runs over a newly
		// created attachment).
		var data []byte
		if v, ok := att.attachProps.Get(mapi.PrAttachDataBin); ok {
			data, _ = v.([]byte)
		}
		var method int32
		if v, ok := att.attachProps.Get(mapi.PrAttachMethod); ok {
			method, _ = v.(int32)
		}
		if method != int32(mapi.AttachEmbeddedMsg) || len(data) == 0 {
			if flags&mapiCreate == 0 {
				writeErr(out, ropOpenEmbeddedMessage, ohindex, ecNotFound)
			} else {
				writeErr(out, ropOpenEmbeddedMessage, ohindex, ecNotSupported)
			}
			return true
		}
		emb, err := oxcmail.Import(data, oxcmail.Options{})
		if err != nil {
			writeErr(out, ropOpenEmbeddedMessage, ohindex, ecError)
			return true
		}
		s.openEmbeddedResponse(out, handles, ohindex, att.store, &embeddedMessage{msg: emb})
		return true

	case kindAttachWrite:
		// Compose a new embedded message on a freshly created attachment. Without
		// MAPI_CREATE there is nothing to open yet (no-auto-create).
		if flags&mapiCreate == 0 {
			writeErr(out, ropOpenEmbeddedMessage, ohindex, ecNotFound)
			return true
		}
		s.openEmbeddedResponse(out, handles, ohindex, att.store,
			&embeddedMessage{msg: &oxcmail.Message{}, writeback: att.attachW})
		return true

	default:
		writeErr(out, ropOpenEmbeddedMessage, ohindex, ecNotSupported)
		return true
	}
}

// openEmbeddedResponse registers the embedded message object under the output
// handle and writes the RopOpenEmbeddedMessage success response: a Reserved byte
// and a MessageId (synthesized from the handle) ahead of the same tail as
// RopOpenMessage.
func (s *Session) openEmbeddedResponse(out *ext.Push, handles []uint32, ohindex uint8, store *objectstore.Store, emb *embeddedMessage) {
	h := s.alloc(&object{kind: kindEmbedded, store: store, embedded: emb})
	setHandle(handles, ohindex, h)

	out.Uint8(ropOpenEmbeddedMessage)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint8(0)                                     // Reserved (always 0)
	out.Uint64(uint64(mapi.MakeEIDEx(1, uint64(h)))) // MessageId (synthesized; re-addressed by handle)
	out.Uint8(0)                                     // HasNamedProperties (v1 advertises none)
	pushTypedString(out, stringProp(emb.msg.Props, mapi.PrSubjectPrefix))
	pushTypedString(out, stringProp(emb.msg.Props, mapi.PrNormalizedSubject))
	out.Uint16(0)         // RecipientCount (v1: inline recipient table deferred)
	_ = out.PropTags(nil) // RecipientColumns (empty proptag array)
	out.Uint8(0)          // RowCount
}

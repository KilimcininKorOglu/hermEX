package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
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
// attachment's PR_ATTACH_DATA_BIN (the proven oxcmail path), so the handler imports
// those bytes into an in-memory message and serves the read ROPs from it. When the
// attachment carries no encapsulated message and MAPI_CREATE is not requested it
// reports MAPI_E_NOT_FOUND, matching the reference (no auto-create); composing a new
// embedded message (MAPI_CREATE) is not yet supported.
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
	if att == nil || att.kind != kindAttachment {
		writeErr(out, ropOpenEmbeddedMessage, ohindex, ecNotSupported)
		return true
	}

	var data []byte
	if v, ok := att.attachProps.Get(mapi.PrAttachDataBin); ok {
		data, _ = v.([]byte)
	}
	var method int32
	if v, ok := att.attachProps.Get(mapi.PrAttachMethod); ok {
		method, _ = v.(int32)
	}
	// The embedded message lives behind a method-5 (afEmbeddedMessage) attachment,
	// its bytes being the encapsulated RFC822 message. Anything else has no embedded
	// message to open. The reference does not auto-create: without MAPI_CREATE that
	// is MAPI_E_NOT_FOUND. Composing a new embedded message (MAPI_CREATE) is deferred
	// — report it honestly rather than open an empty unwritable message.
	if method != int32(mapi.AttachEmbeddedMsg) || len(data) == 0 {
		if flags&mapiCreate == 0 {
			writeErr(out, ropOpenEmbeddedMessage, ohindex, ecNotFound)
			return true
		}
		writeErr(out, ropOpenEmbeddedMessage, ohindex, ecNotSupported)
		return true
	}

	emb, err := oxcmail.Import(data, oxcmail.Options{})
	if err != nil {
		writeErr(out, ropOpenEmbeddedMessage, ohindex, ecError)
		return true
	}
	h := s.alloc(&object{kind: kindEmbedded, store: att.store, embedded: &embeddedMessage{msg: emb}})
	setHandle(handles, ohindex, h)

	out.Uint8(ropOpenEmbeddedMessage)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	out.Uint8(0)                                     // Reserved (always 0)
	out.Uint64(uint64(mapi.MakeEIDEx(1, uint64(h)))) // MessageId (synthesized; re-addressed by handle)
	out.Uint8(0)                                     // HasNamedProperties (v1 advertises none)
	pushTypedString(out, stringProp(emb.Props, mapi.PrSubjectPrefix))
	pushTypedString(out, stringProp(emb.Props, mapi.PrNormalizedSubject))
	out.Uint16(0)         // RecipientCount (v1: inline recipient table deferred)
	_ = out.PropTags(nil) // RecipientColumns (empty proptag array)
	out.Uint8(0)          // RowCount
	return true
}

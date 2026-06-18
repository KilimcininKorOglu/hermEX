package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// MS-OXCNOTIF NotificationType flags ([MS-OXCNOTIF] 2.2.1.1, NotificationFlags).
// The low 12 bits select the notification type (exactly one set per event); the
// high bits are modifiers that ride in the same 16-bit word and gate which
// optional fields the RopNotify body carries. The values are the wire contract —
// see the internal spec §1/§3.
const (
	fnevNewMail        uint16 = 0x0002
	fnevObjectCreated  uint16 = 0x0004
	fnevObjectDeleted  uint16 = 0x0008
	fnevObjectModified uint16 = 0x0010
	fnevObjectMoved    uint16 = 0x0020
	fnevObjectCopied   uint16 = 0x0040
	fnevSearchComplete uint16 = 0x0080
	fnevTableModified  uint16 = 0x0100 // emsmdb-internal; not client-subscribable

	nfExtended  uint16 = 0x0400 // server-internal sentinel; never emitted on the wire
	nfHasTotal  uint16 = 0x1000 // gate: total_count present
	nfHasUnread uint16 = 0x2000 // gate: unread_count present
	nfBySearch  uint16 = 0x4000 // event arrived via a search folder
	nfByMessage uint16 = 0x8000 // message-level event (vs folder-level)
)

// TableEventType sub-codes, present only when fnevTableModified is set
// ([MS-OXCNOTIF] 2.2.1.4.1, TableEventType).
const (
	tableChanged            uint16 = 0x0001
	tableRowAdded           uint16 = 0x0003
	tableRowDeleted         uint16 = 0x0004
	tableRowModified        uint16 = 0x0005
	tableRestrictionChanged uint16 = 0x0007
)

// notification is one server-push event destined for a subscription. flags (the
// NotificationFlags word) selects the type and gates which other fields the wire
// body carries; the per-type field sets are in the internal spec §1. All
// folder/message ids are full wire EIDs (mapi.MakeEIDEx), not bare counters.
type notification struct {
	flags uint16

	// table events (flags has fnevTableModified)
	tableEvent    uint16
	rowFolderID   uint64
	rowMessageID  uint64
	rowInstance   uint32
	afterFolderID uint64
	afterRowID    uint64
	afterInstance uint32
	rowData       []byte

	// object events
	folderID     uint64
	messageID    uint64
	parentID     uint64
	oldFolderID  uint64
	oldMessageID uint64
	oldParentID  uint64
	proptags     []mapi.PropTag
	totalCount   uint32
	unreadCount  uint32

	// new mail
	msgFlags uint32
	msgClass string
}

// pushNotify serializes a RopNotify response ([MS-OXCNOTIF] 2.2.1.4.1) for the
// subscription identified by handle. The field order and the bit-gated presence
// of every optional field are the wire contract (the internal spec §3) and
// must reproduce the reference byte-for-byte. The push buffer must carry FlagUTF16
// — the ROP dispatch buffer does — so the new-mail MessageClass serializes as
// UTF-16LE. It returns an error only when a variable-length field overflows its
// 16-bit length prefix.
func pushNotify(out *ext.Push, handle uint32, logonID uint8, n *notification) error {
	out.Uint8(ropNotify)
	out.Uint32(handle)
	out.Uint8(logonID)
	out.Uint16(n.flags)

	if n.flags&fnevTableModified != 0 {
		out.Uint16(n.tableEvent)
		am := n.tableEvent == tableRowAdded || n.tableEvent == tableRowModified
		amd := am || n.tableEvent == tableRowDeleted
		if amd {
			out.Uint64(n.rowFolderID)
		}
		if amd && n.flags&nfByMessage != 0 {
			out.Uint64(n.rowMessageID)
			out.Uint32(n.rowInstance)
		}
		if am {
			out.Uint64(n.afterFolderID)
		}
		if am && n.flags&nfByMessage != 0 {
			out.Uint64(n.afterRowID)
			out.Uint32(n.afterInstance)
		}
		if am {
			if err := out.BinShort(n.rowData); err != nil {
				return err
			}
		}
	}
	if n.flags&(fnevTableModified|nfExtended) == 0 {
		out.Uint64(n.folderID)
	}
	if n.flags&(fnevTableModified|nfExtended|nfByMessage) == nfByMessage {
		out.Uint64(n.messageID)
	}
	// ParentId rides only on object create/delete/move/copy, and only when the
	// search-folder and message-level bits agree (the reference's xnor gate).
	if n.flags&(fnevObjectCreated|fnevObjectDeleted|fnevObjectMoved|fnevObjectCopied) != 0 &&
		(n.flags&nfBySearch != 0) == (n.flags&nfByMessage != 0) {
		out.Uint64(n.parentID)
	}
	if n.flags&(fnevObjectMoved|fnevObjectCopied) != 0 {
		out.Uint64(n.oldFolderID)
	}
	if n.flags&(fnevObjectMoved|fnevObjectCopied) != 0 && n.flags&nfByMessage != 0 {
		out.Uint64(n.oldMessageID)
	}
	if n.flags&(fnevObjectMoved|fnevObjectCopied) != 0 && n.flags&nfByMessage == 0 {
		out.Uint64(n.oldParentID)
	}
	if n.flags&(fnevObjectCreated|fnevObjectModified) != 0 {
		if err := out.PropTags(n.proptags); err != nil {
			return err
		}
	}
	if n.flags&nfHasTotal != 0 {
		out.Uint32(n.totalCount)
	}
	if n.flags&nfHasUnread != 0 {
		out.Uint32(n.unreadCount)
	}
	if n.flags&fnevNewMail != 0 {
		out.Uint32(n.msgFlags)
		out.Uint8(1) // UnicodeFlag: the MessageClass below is UTF-16LE
		out.Unicode(n.msgClass)
	}
	return nil
}

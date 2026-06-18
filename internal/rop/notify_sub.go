package rop

import "hermex/internal/mapi"

// subscription is a client's registered interest in mailbox events
// (RopRegisterNotification, [MS-OXCNOTIF] 2.2.1.2), held in the session handle
// table. Folder and message ids are objectstore ids — the request EIDs are decoded
// once at registration — so matching an event is a plain compare. handle and
// logonID are echoed back as the RopNotify NotificationHandle/LogonId when an event
// is delivered.
type subscription struct {
	handle     uint32
	logonID    uint8
	types      uint8 // NotificationTypes bitmask (the fnev* low byte)
	wholeStore bool
	folderID   int64 // objectstore id; 0 for a whole-store subscription
	messageID  int64 // objectstore id; 0 for a whole-store or folder subscription
}

// matches reports whether this subscription receives an event of typeBit for the
// given classify scope. The match rule is [MS-OXCNOTIF]'s: a type-bit overlap, then
// either a whole-store subscription or an exact (folder, message) scope match. The
// scope is the per-event one the emitter uses (see classifyScope), so a whole-store
// subscription sees every event, a folder subscription (messageID 0) sees creates
// in its folder, and a message subscription sees modifies/deletes of its message.
// folderID and scopeMessageID are objectstore ids.
func (s subscription) matches(folderID, scopeMessageID int64, typeBit uint8) bool {
	if s.types&typeBit == 0 {
		return false
	}
	return s.wholeStore || (s.folderID == folderID && s.messageID == scopeMessageID)
}

// classifyScope returns the scope message id a content notification is matched
// against, mirroring the reference emitters' asymmetric convention
// (the internal spec §6): a create or new-mail is folder-scoped (id 0 — no
// message-specific subscription can pre-exist a create), while a modify or delete is
// message-scoped. The event's message id is recovered from the notification's wire
// EID; the type bit is the low byte of the NotificationFlags. The caller supplies the
// folder id to matches (it owns the folder the poll ran against).
func classifyScope(n *notification) (scopeMessageID int64, typeBit uint8) {
	typeBit = uint8(n.flags)
	if n.flags&(fnevObjectCreated|fnevNewMail) != 0 {
		return 0, typeBit
	}
	return int64(mapi.EID(n.messageID).GCValue()), typeBit
}

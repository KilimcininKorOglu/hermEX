package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// ropRegisterNotification handles RopRegisterNotification ([MS-OXCNOTIF] 2.2.1.2;
// request format in the internal spec §2): the client registers an interest
// in mailbox events — whole-store, or scoped to a folder or a single message — and
// the server allocates a subscription object whose handle it echoes back as the
// NotificationHandle of every RopNotify the subscription later delivers. The ROP has
// no response body, only the standard 6-byte head, and the response HandleIndex is
// the OutputHandleIndex the new handle was placed at (mirroring the reference, whose
// rshead->hindex = ohindex).
//
// hermEX has no central store daemon to push from, so events are detected by polling
// the shared store (the internal spec §9). That makes the folder baseline
// snapshot load-bearing here: it MUST be taken at registration so the first poll
// diffs against the state at subscribe time and reports nothing for messages that
// already existed — otherwise the first drain would flood the client with every
// existing message as an ObjectCreated. v1 services folder- and message-scoped
// subscriptions (both poll the subscribed folder; the classifier filters per scope).
// A whole-store subscription is accepted and given a handle but is not yet serviced
// by the poll — the all-folders sweep it needs lands in a later increment — so its
// baseline is left nil.
func (s *Session) ropRegisterNotification(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	ohindex, e1 := p.Uint8()   // OutputHandleIndex
	ntypes, e2 := p.Uint8()    // NotificationTypes (one byte; subscribable types fit 0x02..0x80)
	_, e3 := p.Uint8()         // Reserved
	wantWhole, e4 := p.Uint8() // WantWholeStore
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	wholeStore := wantWhole != 0
	var folderID, messageID int64
	if !wholeStore {
		folderEID, e5 := p.Uint64()  // FolderId
		messageEID, e6 := p.Uint64() // MessageId
		if e5 != nil || e6 != nil {
			return false
		}
		folderID = int64(mapi.EID(folderEID).GCValue())
		messageID = int64(mapi.EID(messageEID).GCValue())
	}

	parent := s.get(handleAt(handles, hindex))
	if parent == nil || parent.store == nil {
		writeErr(out, ropRegisterNotification, ohindex, ecError)
		return true
	}

	obj := &object{
		kind:  kindSubscription,
		store: parent.store,
		sub: subscription{
			logonID:    0, // a single logon in v1 (the dispatch discards the per-ROP LogonId)
			types:      ntypes,
			wholeStore: wholeStore,
			folderID:   folderID,
			messageID:  messageID,
		},
	}
	// Baseline the subscribed folder at registration (see the doc comment). A
	// message-scoped subscription baselines the same folder — the poll diffs the
	// folder and the classifier narrows to the message.
	if !wholeStore {
		snap, err := parent.store.FolderMessageChangeNumbers(folderID)
		if err != nil {
			writeErr(out, ropRegisterNotification, ohindex, ecError)
			return true
		}
		obj.subSnapshot = snap
	}

	h := s.alloc(obj)
	obj.sub.handle = h
	setHandle(handles, ohindex, h)

	out.Uint8(ropRegisterNotification)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

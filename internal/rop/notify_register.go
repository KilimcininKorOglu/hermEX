package rop

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ropRegisterNotification handles RopRegisterNotification ([MS-OXCNOTIF] 2.2.1.2;
// request format in the internal spec §2): the client registers an interest
// in mailbox events, whole-store, or scoped to a folder or a single message, and
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
// already existed, otherwise the first drain would flood the client with every
// existing message as an ObjectCreated. A folder- or message-scoped subscription
// baselines and polls its one folder (the classifier filters per scope); a
// whole-store subscription baselines every content folder and is polled across all of
// them. A whole-store subscription also baselines the folder hierarchy itself (each
// folder's parent and message counts), so the first poll likewise reports no spurious
// folder created/deleted/modified for the tree that existed at subscribe time.
func (s *Session) ropRegisterNotification(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	req, framed := pullRegisterNotificationRequest(p)
	if !framed {
		return false
	}
	ohindex := req.ohindex
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
			types:      req.types,
			wholeStore: req.wholeStore,
			folderID:   req.folderID,
			messageID:  req.messageID,
		},
	}
	if err := baselineSubscription(obj, parent.store, req); err != nil {
		writeErr(out, ropRegisterNotification, ohindex, ecError)
		return true
	}

	h := s.alloc(obj)
	obj.sub.handle = h
	setHandle(handles, ohindex, h)

	out.Uint8(ropRegisterNotification)
	out.Uint8(ohindex)
	out.Uint32(ecSuccess)
	return true
}

// registerNotificationRequest is a decoded RopRegisterNotification request. A
// whole-store subscription carries no ids, so folderID and messageID are read
// only when it is scoped.
type registerNotificationRequest struct {
	ohindex    uint8
	types      uint8
	wholeStore bool
	folderID   int64
	messageID  int64
}

func pullRegisterNotificationRequest(p *ext.Pull) (registerNotificationRequest, bool) {
	var req registerNotificationRequest
	ohindex, e1 := p.Uint8()   // OutputHandleIndex
	ntypes, e2 := p.Uint8()    // NotificationTypes (one byte; subscribable types fit 0x02..0x80)
	_, e3 := p.Uint8()         // Reserved
	wantWhole, e4 := p.Uint8() // WantWholeStore
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return req, false
	}
	req = registerNotificationRequest{ohindex: ohindex, types: ntypes, wholeStore: wantWhole != 0}
	if req.wholeStore {
		return req, true
	}
	folderEID, e5 := p.Uint64()  // FolderId
	messageEID, e6 := p.Uint64() // MessageId
	if e5 != nil || e6 != nil {
		return req, false
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	req.folderID = int64(mapi.EID(folderEID).GCValue())
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	req.messageID = int64(mapi.EID(messageEID).GCValue())
	return req, true
}

// baselineSubscription records the mailbox state at registration, so the first
// poll reports nothing that already existed. A folder- or message-scoped
// subscription baselines its one folder: the poll diffs that folder and the
// classifier narrows to the message. A whole-store subscription baselines every
// content folder and the folder hierarchy, so its first poll reports no spurious
// create, delete or modify for the tree present at subscribe time.
func baselineSubscription(obj *object, store *objectstore.Store, req registerNotificationRequest) error {
	if !req.wholeStore {
		snap, err := store.FolderMessageChangeNumbers(req.folderID)
		if err != nil {
			return err
		}
		obj.subSnapshot = snap
		return nil
	}
	folders, err := store.ListFolders()
	if err != nil {
		return err
	}
	obj.subFolders = make(map[int64]folderSnapshot, len(folders))
	for _, f := range folders {
		snap, err := store.FolderMessageChangeNumbers(f.ID)
		if err != nil {
			return err
		}
		obj.subFolders[f.ID] = snap
	}
	meta, err := folderMetaSnapshot(store, folders)
	if err != nil {
		return err
	}
	obj.subFolderMeta = meta
	return nil
}

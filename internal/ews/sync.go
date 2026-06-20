package ews

import (
	"encoding/xml"
	"maps"
	"net/http"
	"slices"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// maxSyncBatch caps the number of changes a single SyncFolderItems response
// carries; the client re-syncs with the new SyncState to fetch the rest.
const maxSyncBatch = 512

// --- request ---

type syncFolderItemsRequest struct {
	SyncFolderID       folderRefs `xml:"SyncFolderId"`
	SyncState          string     `xml:"SyncState"`
	MaxChangesReturned int        `xml:"MaxChangesReturned"`
}

// --- response ---

type syncItemsResponse struct {
	XMLName  xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderItemsResponse"`
	Messages []syncItemsResponseMessage `xml:"ResponseMessages>SyncFolderItemsResponseMessage"`
}

type syncItemsResponseMessage struct {
	ResponseClass           string       `xml:"ResponseClass,attr"`
	ResponseCode            string       `xml:"ResponseCode"`
	SyncState               string       `xml:"SyncState,omitempty"`
	IncludesLastItemInRange bool         `xml:"IncludesLastItemInRange"`
	Changes                 *itemChanges `xml:"Changes,omitempty"`
}

// itemChanges is the m:Changes wrapper (messages namespace, inherited); the
// individual change elements are in the types namespace (t:Create/t:Update/
// t:Delete), which is how clients key the change type.
type itemChanges struct {
	Create []itemChange       `xml:"http://schemas.microsoft.com/exchange/services/2006/types Create"`
	Update []itemChange       `xml:"http://schemas.microsoft.com/exchange/services/2006/types Update"`
	Delete []deleteItemChange `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete"`
}

type itemChange struct {
	Message oxews.Message
}

type deleteItemChange struct {
	ItemID oxews.ItemIDElem `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
}

// handleSyncFolderItems answers SyncFolderItems (the keystone): it diffs the live
// folder against the per-folder item snapshot to emit Create/Update/Delete. An
// empty SyncState is a fresh prime (every item is a Create); a matching one is a
// delta; a stale one is rejected so the client re-primes. Change numbers cannot
// drive this — they are INSERT-only, so flag toggles and deletes are invisible
// to them; the snapshot diff is the only channel-agnostic detector.
func (s *Server) handleSyncFolderItems(w http.ResponseWriter, inner []byte, sess *session) {
	s.icsSync(sess.user, "folder-items")
	var req syncFolderItemsRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "SyncFolderItems: "+err.Error())
		return
	}
	targets := resolveTargets(req.SyncFolderID)
	if len(targets) == 0 {
		writeSyncItemsError(w, "ErrorInvalidRequest")
		return
	}
	if !targets[0].ok {
		writeSyncItemsError(w, targets[0].code)
		return
	}
	fid := targets[0].fid

	cache := s.newStoreCache()
	defer cache.closeAll()
	// Items come from the target mailbox; syncing a delegated folder requires read access.
	st, _, isOwn, code := cache.open(sess, targets[0].mailbox)
	if code != "" {
		writeSyncItemsError(w, code)
		return
	}
	if !isOwn {
		rights, perr := st.ResolvePermission(fid, sess.user)
		if perr != nil {
			writeSOAPFault(w, "ErrorInternalServerError", perr.Error())
			return
		}
		if rights&mapi.FrightsReadAny == 0 {
			writeSyncItemsError(w, "ErrorAccessDenied")
			return
		}
	}
	// The sync state is kept in the caller's OWN store, keyed per (target, folder), so a
	// delegate's sync cursor never collides with the target owner's own state; item ids
	// carry the target mailbox so the client reopens it on a follow-up.
	idMailbox := ""
	stateStore := st
	stateKey := strconv.FormatInt(fid, 10)
	if !isOwn {
		idMailbox = targets[0].mailbox
		stateKey = targets[0].mailbox + ":" + stateKey
		own, _, _, oc := cache.open(sess, "")
		if oc != "" {
			writeSyncItemsError(w, oc)
			return
		}
		stateStore = own
	}
	state, err := loadState(stateStore)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	fstate := state.folder(stateKey)

	// Choose the baseline snapshot from the supplied SyncState.
	var snap map[string]int64
	switch req.SyncState {
	case "":
		snap = nil // fresh prime
	case fstate.SyncState:
		snap = fstate.Items
	default:
		writeSyncItemsError(w, "ErrorInvalidSyncStateData")
		return
	}

	live, err := st.ListMessages(fid)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}

	max := req.MaxChangesReturned
	if max <= 0 || max > maxSyncBatch {
		max = maxSyncBatch
	}

	// Diff: live items not in the snapshot are creates; flag-changed items are
	// updates; snapshot items absent from live are deletes. Live is UID-ordered;
	// deletes are sorted for a deterministic batch boundary.
	type pending struct {
		kind string
		id   string
		flag int64
		info objectstore.MessageInfo
	}
	liveSet := make(map[string]bool, len(live))
	var all []pending
	for _, info := range live {
		id := oxews.EncodeItemID(oxews.ItemID{FolderID: fid, MessageID: info.ID, UID: info.UID, Mailbox: idMailbox})
		liveSet[id] = true
		if prev, ok := snap[id]; !ok {
			all = append(all, pending{kind: "create", id: id, flag: info.Flags, info: info})
		} else if prev != info.Flags {
			all = append(all, pending{kind: "update", id: id, flag: info.Flags, info: info})
		}
	}
	var delIDs []string
	for id := range snap {
		if !liveSet[id] {
			delIDs = append(delIDs, id)
		}
	}
	slices.Sort(delIDs)
	for _, id := range delIDs {
		all = append(all, pending{kind: "delete", id: id})
	}

	includesLast := true
	if len(all) > max {
		all = all[:max]
		includesLast = false
	}

	// Advance a fresh copy of the snapshot for the sent changes only; unsent
	// changes stay in the old snapshot so the next sync reports them again.
	newSnap := make(map[string]int64, len(snap))
	maps.Copy(newSnap, snap)
	changes := &itemChanges{}
	for _, p := range all {
		switch p.kind {
		case "create":
			changes.Create = append(changes.Create, itemChange{Message: itemSummary(st, fid, p.info, idMailbox)})
			newSnap[p.id] = p.flag
		case "update":
			changes.Update = append(changes.Update, itemChange{Message: itemSummary(st, fid, p.info, idMailbox)})
			newSnap[p.id] = p.flag
		case "delete":
			changes.Delete = append(changes.Delete, deleteItemChange{ItemID: oxews.ItemIDElem{ID: p.id}})
			delete(newSnap, p.id)
		}
	}

	newToken := nextSyncState(fstate.SyncState)
	fstate.SyncState = newToken
	fstate.Items = newSnap
	if err := saveState(stateStore, state); err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}

	writeResponse(w, syncItemsResponse{Messages: []syncItemsResponseMessage{{
		ResponseClass:           "Success",
		ResponseCode:            "NoError",
		SyncState:               newToken,
		IncludesLastItemInRange: includesLast,
		Changes:                 changes,
	}}})
}

// writeSyncItemsError writes a SyncFolderItems error response message.
func writeSyncItemsError(w http.ResponseWriter, code string) {
	writeResponse(w, syncItemsResponse{Messages: []syncItemsResponseMessage{{
		ResponseClass: "Error",
		ResponseCode:  code,
	}}})
}

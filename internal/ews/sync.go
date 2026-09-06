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
// drive this, they are INSERT-only, so flag toggles and deletes are invisible
// to them; the snapshot diff is the only channel-agnostic detector.
func (s *Server) handleSyncFolderItems(w http.ResponseWriter, inner []byte, sess *session) {
	s.icsSync(sess.user, "folder-items")
	var req syncFolderItemsRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "SyncFolderItems: invalid request", err)
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()
	ctx, code, err := s.prepareItemSync(cache, sess, req.SyncFolderID)
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}
	if code != "" {
		writeSyncItemsError(w, code)
		return
	}

	state, err := loadState(ctx.stateStore)
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}
	fstate := state.folder(ctx.stateKey)
	snap, ok := baselineSnapshot(req.SyncState, fstate)
	if !ok {
		writeSyncItemsError(w, "ErrorInvalidSyncStateData")
		return
	}

	live, err := ctx.st.ListMessages(ctx.fid)
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}
	all := pendingItemChanges(live, snap, ctx)
	includesLast := true
	if max := syncBatchLimit(req.MaxChangesReturned); len(all) > max {
		all = all[:max]
		includesLast = false
	}
	changes, newSnap := renderItemChanges(all, snap, ctx)

	newToken := nextSyncState(fstate.SyncState)
	fstate.SyncState = newToken
	fstate.Items = newSnap
	if err := saveState(ctx.stateStore, state); err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
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

// itemSyncContext is where one SyncFolderItems reads its items and keeps its
// cursor. The two stores differ when the caller syncs a delegated folder.
type itemSyncContext struct {
	st         *objectstore.Store // holds the items
	fid        int64
	idMailbox  string             // stamped into item ids, empty for the caller's own mailbox
	stateStore *objectstore.Store // holds the sync cursor
	stateKey   string
}

// prepareItemSync resolves the requested folder, opens the store that holds its
// items, and locates the sync cursor. Items come from the target mailbox, so
// syncing a delegated folder requires read access. The cursor is kept in the
// caller's OWN store, keyed per (target, folder), so a delegate's cursor never
// collides with the target owner's own state, and item ids carry the target
// mailbox so the client reopens it on a follow-up.
//
// A non-empty code is a response-message error; an error is a fault.
func (s *Server) prepareItemSync(cache *storeCache, sess *session, refs folderRefs) (itemSyncContext, string, error) {
	targets := resolveTargets(refs)
	if len(targets) == 0 {
		return itemSyncContext{}, "ErrorInvalidRequest", nil
	}
	if !targets[0].ok {
		return itemSyncContext{}, targets[0].code, nil
	}
	fid := targets[0].fid
	st, _, isOwn, code := cache.open(sess, targets[0].mailbox)
	if code != "" {
		return itemSyncContext{}, code, nil
	}
	ctx := itemSyncContext{st: st, fid: fid, stateStore: st, stateKey: strconv.FormatInt(fid, 10)}
	if isOwn {
		return ctx, "", nil
	}
	if code, err := folderReadAccess(st, fid, sess.user); code != "" || err != nil {
		return itemSyncContext{}, code, err
	}
	ctx.idMailbox = targets[0].mailbox
	ctx.stateKey = targets[0].mailbox + ":" + ctx.stateKey
	own, _, _, oc := cache.open(sess, "")
	if oc != "" {
		return itemSyncContext{}, oc, nil
	}
	ctx.stateStore = own
	return ctx, "", nil
}

// folderReadAccess reports the response code refusing a caller who lacks read
// access to a folder they do not own; an empty code means access is granted.
func folderReadAccess(st *objectstore.Store, fid int64, user string) (string, error) {
	rights, err := st.ResolvePermission(fid, user)
	if err != nil {
		return "", err
	}
	if rights&mapi.FrightsReadAny == 0 {
		return "ErrorAccessDenied", nil
	}
	return "", nil
}

// baselineSnapshot chooses the snapshot the diff runs against: an empty SyncState
// is a fresh prime, the current token continues from its snapshot, and any other
// token is a state this server did not issue.
func baselineSnapshot(syncState string, fstate *folderItemState) (map[string]int64, bool) {
	switch syncState {
	case "":
		return nil, true
	case fstate.SyncState:
		return fstate.Items, true
	default:
		return nil, false
	}
}

// syncBatchLimit clamps the client's MaxChangesReturned to the server's batch cap.
func syncBatchLimit(requested int) int {
	if requested <= 0 || requested > maxSyncBatch {
		return maxSyncBatch
	}
	return requested
}

// pendingItem is one change SyncFolderItems has to report.
type pendingItem struct {
	kind string
	id   string
	flag int64
	info objectstore.MessageInfo
}

// pendingItemChanges diffs the live folder against the snapshot: a live item the
// snapshot does not hold is a create, a flag-changed item an update, and a
// snapshot item no longer live a delete. Live is UID-ordered, and the deletes are
// sorted, so the batch boundary is deterministic.
func pendingItemChanges(live []objectstore.MessageInfo, snap map[string]int64, ctx itemSyncContext) []pendingItem {
	liveSet := make(map[string]bool, len(live))
	var all []pendingItem
	for _, info := range live {
		id := oxews.EncodeItemID(oxews.ItemID{FolderID: ctx.fid, MessageID: info.ID, UID: info.UID, Mailbox: ctx.idMailbox})
		liveSet[id] = true
		if prev, ok := snap[id]; !ok {
			all = append(all, pendingItem{kind: "create", id: id, flag: info.Flags, info: info})
		} else if prev != info.Flags {
			all = append(all, pendingItem{kind: "update", id: id, flag: info.Flags, info: info})
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
		all = append(all, pendingItem{kind: "delete", id: id})
	}
	return all
}

// renderItemChanges builds the response changes and advances a fresh copy of the
// snapshot for the sent changes only, so unsent changes stay in the old snapshot
// and the next sync reports them again.
func renderItemChanges(all []pendingItem, snap map[string]int64, ctx itemSyncContext) (*itemChanges, map[string]int64) {
	newSnap := make(map[string]int64, len(snap))
	maps.Copy(newSnap, snap)
	changes := &itemChanges{}
	for _, p := range all {
		switch p.kind {
		case "create":
			changes.Create = append(changes.Create, itemChange{Message: itemSummary(ctx.st, ctx.fid, p.info, ctx.idMailbox)})
			newSnap[p.id] = p.flag
		case "update":
			changes.Update = append(changes.Update, itemChange{Message: itemSummary(ctx.st, ctx.fid, p.info, ctx.idMailbox)})
			newSnap[p.id] = p.flag
		case "delete":
			changes.Delete = append(changes.Delete, deleteItemChange{ItemID: oxews.ItemIDElem{ID: p.id}})
			delete(newSnap, p.id)
		}
	}
	return changes, newSnap
}

// writeSyncItemsError writes a SyncFolderItems error response message.
func writeSyncItemsError(w http.ResponseWriter, code string) {
	writeResponse(w, syncItemsResponse{Messages: []syncItemsResponseMessage{{
		ResponseClass: "Error",
		ResponseCode:  code,
	}}})
}

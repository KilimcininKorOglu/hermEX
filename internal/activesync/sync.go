package activesync

import (
	"net/http"
	"sort"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// Window bounds for a Sync batch. WindowSize caps how many server changes a
// single response carries; the rest follow behind <MoreAvailable/>.
const (
	defaultWindowSize = 100
	maxWindowSize     = 512
)

// Sync collection status codes (MS-ASCMD): 1 success, 3 invalid sync key
// (forces a re-prime), 4 malformed request.
const (
	syncStatusOK         = 1
	syncStatusInvalidKey = 3
	syncStatusBadRequest = 4
)

// handleSync answers the Sync command: per collection it applies the client's
// changes, then streams the server's own changes computed by diffing the live
// folder against the device's last-synced snapshot.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	collections := root.Child(wbxml.ASCollections)
	if collections == nil {
		http.Error(w, "Sync without collections", http.StatusBadRequest)
		return
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	state, err := loadState(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dev := state.device(sess.req.deviceID)

	var out []*wbxml.Node
	for _, c := range collections.Children {
		if c.Tag != wbxml.ASCollection {
			continue
		}
		resp, err := syncCollection(st, dev, c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, resp)
	}
	if err := saveState(st, state); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWBXML(w, wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections, out...)))
}

// pendingChange is one server-to-client change awaiting windowing.
type pendingChange struct {
	kind int // 0 add, 1 change, 2 delete
	sid  string
	m    objectstore.MessageInfo
}

const (
	changeAdd = iota
	changeChange
	changeDelete
)

// syncCollection processes one <Collection>: it primes on sync key 0, rejects a
// stale key with Status 3, otherwise applies the client's commands and streams
// the snapshot-diff changes (capped at the window).
func syncCollection(st *objectstore.Store, dev *deviceState, c *wbxml.Node) (*wbxml.Node, error) {
	collID := c.ChildText(wbxml.ASCollectionID)
	clientKey := c.ChildText(wbxml.ASSyncKey)
	window := parseWindow(c.ChildText(wbxml.ASWindowSize))

	folderID, err := strconv.ParseInt(collID, 10, 64)
	if err != nil {
		return syncStatus(collID, "0", syncStatusBadRequest), nil
	}
	cstate := dev.collection(collID)

	// Prime: reset the snapshot and issue the first key, returning no items.
	if clientKey == "0" {
		cstate.SyncKey = nextSyncKey("0")
		cstate.Items = map[string]int64{}
		return syncResponse(collID, cstate.SyncKey, nil, false), nil
	}
	// A key that is not the one we last issued forces a re-prime (Status 3). v1
	// does not replay a dropped response; the client recovers by re-priming.
	if clientKey == "" || clientKey != cstate.SyncKey {
		return syncStatus(collID, "0", syncStatusInvalidKey), nil
	}
	if cstate.Items == nil {
		cstate.Items = map[string]int64{}
	}

	// Calendar collections take a separate path: their items are read from the
	// object store (never the IMAP index) and versioned by change number, not IMAP
	// flags. This increment streams server-side appointment changes; client-side
	// calendar edits are a later increment, so its client commands are not applied.
	if folderID == int64(mapi.PrivateFIDCalendar) {
		cmds, more, err := calendarChanges(st, folderID, cstate, window)
		if err != nil {
			return nil, err
		}
		cstate.SyncKey = nextSyncKey(clientKey)
		return syncResponse(collID, cstate.SyncKey, cmds, more), nil
	}

	// Apply the client's commands first, folding each into the snapshot so the
	// diff below does not echo the client's own change back to it.
	applyClientCommands(st, folderID, cstate, c)

	live, err := st.ListMessages(folderID)
	if err != nil {
		return nil, err
	}
	pending := diffSnapshot(cstate.Items, live)

	more := false
	if len(pending) > window {
		pending = pending[:window]
		more = true
	}

	var cmds []*wbxml.Node
	for _, ch := range pending {
		switch ch.kind {
		case changeAdd:
			raw, err := st.GetMessageRaw(folderID, ch.m.UID)
			if err != nil {
				return nil, err
			}
			cmds = append(cmds, wbxml.Elem(wbxml.ASAdd,
				wbxml.Str(wbxml.ASServerID, ch.sid), emailAppData(raw, ch.m, collID, ch.sid)))
			cstate.Items[ch.sid] = ch.m.Flags
		case changeChange:
			cmds = append(cmds, wbxml.Elem(wbxml.ASChange,
				wbxml.Str(wbxml.ASServerID, ch.sid), readAppData(ch.m)))
			cstate.Items[ch.sid] = ch.m.Flags
		case changeDelete:
			cmds = append(cmds, wbxml.Elem(wbxml.ASDelete, wbxml.Str(wbxml.ASServerID, ch.sid)))
			delete(cstate.Items, ch.sid)
		}
	}
	cstate.SyncKey = nextSyncKey(clientKey)
	return syncResponse(collID, cstate.SyncKey, cmds, more), nil
}

// diffSnapshot compares the device's last-synced snapshot to the live folder and
// returns the changes in a deterministic order: adds and flag changes in UID
// order, then deletes in UID order.
func diffSnapshot(snapshot map[string]int64, live []objectstore.MessageInfo) []pendingChange {
	var out []pendingChange
	liveSeen := make(map[string]bool, len(live))
	for _, m := range live {
		sid := strconv.FormatUint(uint64(m.UID), 10)
		liveSeen[sid] = true
		prev, ok := snapshot[sid]
		switch {
		case !ok:
			out = append(out, pendingChange{kind: changeAdd, sid: sid, m: m})
		case prev != m.Flags:
			out = append(out, pendingChange{kind: changeChange, sid: sid, m: m})
		}
	}
	var deletes []string
	for sid := range snapshot {
		if !liveSeen[sid] {
			deletes = append(deletes, sid)
		}
	}
	sort.Slice(deletes, func(i, j int) bool { return lessSID(deletes[i], deletes[j]) })
	for _, sid := range deletes {
		out = append(out, pendingChange{kind: changeDelete, sid: sid})
	}
	return out
}

// applyClientCommands applies the device's Change (read flag) and Delete
// commands to the store, updating the snapshot so the server does not echo them.
func applyClientCommands(st *objectstore.Store, folderID int64, cstate *collectionState, c *wbxml.Node) {
	cmds := c.Child(wbxml.ASCommands)
	if cmds == nil {
		return
	}
	for _, cmd := range cmds.Children {
		sid := cmd.ChildText(wbxml.ASServerID)
		uid64, err := strconv.ParseUint(sid, 10, 32)
		if err != nil {
			continue
		}
		uid := uint32(uid64)
		switch cmd.Tag {
		case wbxml.ASChange:
			data := cmd.Child(wbxml.ASData)
			if data == nil {
				continue
			}
			cur, err := st.MessageFlags(folderID, uid)
			if err != nil {
				continue
			}
			switch data.ChildText(wbxml.EMRead) {
			case "1":
				cur |= objectstore.FlagSeen
			case "0":
				cur &^= objectstore.FlagSeen
			}
			if st.SetMessageFlags(folderID, uid, cur) == nil {
				cstate.Items[sid] = cur
			}
		case wbxml.ASDelete:
			if st.DeleteMessage(folderID, uid) == nil {
				delete(cstate.Items, sid)
			}
		}
	}
}

// emailAppData builds the ApplicationData for an Email-class item: the listing
// properties from the index, the full message as a MIME body, and — when the
// message carries attachments — an AirSyncBase Attachments listing whose
// FileReferences the device fetches through ItemOperations. v1 serves the body
// as MIME (Type 4); honoring fine-grained body preferences is later work.
func emailAppData(raw []byte, m objectstore.MessageInfo, collID, serverID string) *wbxml.Node {
	data := wbxml.Elem(wbxml.ASData,
		wbxml.Str(wbxml.EMSubject, m.Subject),
		wbxml.Str(wbxml.EMFrom, m.Sender),
		wbxml.Str(wbxml.EMDateReceived, m.InternalDate.UTC().Format("2006-01-02T15:04:05.000Z")),
		wbxml.Str(wbxml.EMMessageClass, "IPM.Note"),
		wbxml.Str(wbxml.EMRead, readFlag(m.Flags)),
		wbxml.Elem(wbxml.ABBody,
			wbxml.Str(wbxml.ABType, "4"),
			wbxml.Str(wbxml.ABEstimatedDataSize, strconv.Itoa(len(raw))),
			wbxml.Opaque(wbxml.ABData, raw),
		),
	)
	if atts := attachmentsNode(collID, serverID, messageAttachments(raw)); atts != nil {
		data.Children = append(data.Children, atts)
	}
	return data
}

// readAppData builds the minimal ApplicationData for a flag change: just the
// read state, the only flag v1 tracks.
func readAppData(m objectstore.MessageInfo) *wbxml.Node {
	return wbxml.Elem(wbxml.ASData, wbxml.Str(wbxml.EMRead, readFlag(m.Flags)))
}

func readFlag(flags int64) string {
	if flags&objectstore.FlagSeen != 0 {
		return "1"
	}
	return "0"
}

// syncResponse builds a Status-1 Collection reply with the new key and commands.
func syncResponse(collID, key string, cmds []*wbxml.Node, more bool) *wbxml.Node {
	children := []*wbxml.Node{
		wbxml.Str(wbxml.ASSyncKey, key),
		wbxml.Str(wbxml.ASCollectionID, collID),
		wbxml.Str(wbxml.ASStatus, strconv.Itoa(syncStatusOK)),
	}
	if len(cmds) > 0 {
		children = append(children, wbxml.Elem(wbxml.ASCommands, cmds...))
	}
	if more {
		children = append(children, wbxml.Empty(wbxml.ASMoreAvailable))
	}
	return wbxml.Elem(wbxml.ASCollection, children...)
}

// syncStatus builds a Collection reply carrying only a status (and the key to
// report), e.g. Status 3 with key 0 to force a re-prime.
func syncStatus(collID, key string, status int) *wbxml.Node {
	return wbxml.Elem(wbxml.ASCollection,
		wbxml.Str(wbxml.ASSyncKey, key),
		wbxml.Str(wbxml.ASCollectionID, collID),
		wbxml.Str(wbxml.ASStatus, strconv.Itoa(status)),
	)
}

func parseWindow(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return defaultWindowSize
	}
	if n > maxWindowSize {
		return maxWindowSize
	}
	return n
}

// lessSID orders two numeric server ids numerically.
func lessSID(a, b string) bool {
	ai, _ := strconv.ParseUint(a, 10, 64)
	bi, _ := strconv.ParseUint(b, 10, 64)
	return ai < bi
}

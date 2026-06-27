package activesync

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/wbxml"
)

// Window bounds for a Sync batch. WindowSize caps how many server changes a
// single response carries; the rest follow behind <MoreAvailable/>.
const (
	defaultWindowSize = 100
	maxWindowSize     = 512
)

// Sync collection status codes (MS-ASCMD): 1 success, 3 invalid sync key
// (forces a re-prime), 4 malformed request, 14 invalid wait/heartbeat value.
const (
	syncStatusOK           = 1
	syncStatusInvalidKey   = 3
	syncStatusBadRequest   = 4
	syncStatusWaitInterval = 14
)

// syncHoldCadence is the fallback poll interval during a hanging Sync when the push
// relay is absent or a wake is missed — the degradation floor matching the
// reference's 30s heartbeat poll.
const syncHoldCadence = 30 * time.Second

// Sync HeartbeatInterval bounds (MS-ASCMD): a value outside [60, 3540] seconds is
// Status 14. These are tighter than Ping's bounds (its minimum is 1s), so the Sync
// path validates separately.
const (
	syncMinHeartbeat = 60 * time.Second
	syncMaxHeartbeat = 3540 * time.Second
)

// parseSyncHeartbeat parses a Sync HeartbeatInterval (seconds). ok is false when the
// value is unparseable or outside [60, 3540]; bound is then the nearest acceptable
// value for the Status-14 Limit element.
func parseSyncHeartbeat(s string) (d, bound time.Duration, ok bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, syncMinHeartbeat, false
	}
	d = time.Duration(n) * time.Second
	if d < syncMinHeartbeat {
		return 0, syncMinHeartbeat, false
	}
	if d > syncMaxHeartbeat {
		return 0, syncMaxHeartbeat, false
	}
	return d, 0, true
}

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

	// A hanging Sync carries a HeartbeatInterval (seconds): the client asks the
	// server to hold the response until a change lands. An out-of-range value is
	// Status 14 with the nearest acceptable bound in a Limit element.
	hbText := root.ChildText(wbxml.ASHeartbeatInt)
	var heartbeat time.Duration
	if hbText != "" {
		hb, bound, ok := parseSyncHeartbeat(hbText)
		if !ok {
			writeWBXML(w, syncWaitLimit(bound))
			return
		}
		heartbeat = hb
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

	// Hanging Sync: when a heartbeat is requested, the request carries no client
	// commands, and there are no server changes yet, hold until a change lands
	// (push wake, or the fallback cadence) or the heartbeat expires. On expiry with
	// nothing new the reply is an empty body with Connection: close (MS-ASCMD); the
	// client re-issues and the sync key does not advance.
	if heartbeat > 0 && !syncHasCommands(collections) && !syncHasChanges(st, dev, collections) {
		if !s.holdForSync(r.Context(), sess.mailbox, dev, collections, heartbeat) {
			w.Header().Set("Connection", "close")
			w.WriteHeader(http.StatusOK)
			return
		}
		// Woken with changes: fall through to the normal collect, which the open
		// store observes via a fresh read.
	}

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

// syncWaitLimit builds the Status-14 reply for an out-of-range HeartbeatInterval,
// naming the nearest acceptable bound (in seconds) in a Limit element so the device
// retries with an in-range value.
func syncWaitLimit(bound time.Duration) *wbxml.Node {
	return wbxml.Elem(wbxml.ASSync,
		wbxml.Str(wbxml.ASStatus, strconv.Itoa(syncStatusWaitInterval)),
		wbxml.Str(wbxml.ASLimit, strconv.Itoa(int(bound/time.Second))),
	)
}

// syncHasCommands reports whether any collection carries client commands. A hanging
// Sync only holds for a pure "tell me when something changes" request; a request
// that also sends commands is processed one-shot.
func syncHasCommands(collections *wbxml.Node) bool {
	for _, c := range collections.Children {
		if c.Tag == wbxml.ASCollection && c.Child(wbxml.ASCommands) != nil {
			return true
		}
	}
	return false
}

// syncHasChanges reports whether any watched collection has server changes the
// device has not yet synced. It is a pure read — it never advances a snapshot — so
// it is safe to call repeatedly during a hold.
func syncHasChanges(st *objectstore.Store, dev *deviceState, collections *wbxml.Node) bool {
	for _, c := range collections.Children {
		if c.Tag == wbxml.ASCollection && collectionHasChanges(st, dev, c) {
			return true
		}
	}
	return false
}

// collectionHasChanges diffs one collection's live state against the device's
// snapshot without mutating it. A priming (key 0), unparseable, or stale-key
// collection is not a hold candidate (the normal collect handles those).
func collectionHasChanges(st *objectstore.Store, dev *deviceState, c *wbxml.Node) bool {
	collID := c.ChildText(wbxml.ASCollectionID)
	clientKey := c.ChildText(wbxml.ASSyncKey)
	if clientKey == "0" || clientKey == "" {
		return false
	}
	folderID, err := strconv.ParseInt(collID, 10, 64)
	if err != nil {
		return false
	}
	cstate := dev.collection(collID)
	if clientKey != cstate.SyncKey {
		return false // stale key: let the normal path return Status 3
	}
	if isObjectFolder(folderID) {
		objs, err := st.ListFolderObjects(folderID)
		if err != nil {
			return false
		}
		return objectsDiffer(cstate.Items, objs)
	}
	live, err := st.ListMessages(folderID)
	if err != nil {
		return false
	}
	return len(diffSnapshot(cstate.Items, live)) > 0
}

// objectsDiffer reports whether an object folder's items differ from the device
// snapshot (an add, a change-number bump, or a delete) — the read-only counterpart
// of objectChanges' diff.
func objectsDiffer(snap map[string]int64, objs []objectstore.FolderObject) bool {
	live := make(map[string]bool, len(objs))
	for _, o := range objs {
		sid := strconv.FormatInt(o.ID, 10)
		live[sid] = true
		if prev, ok := snap[sid]; !ok || prev != int64(o.ChangeNumber) {
			return true
		}
	}
	for sid := range snap {
		if !live[sid] {
			return true
		}
	}
	return false
}

// objectChangeCount counts how many of an object folder's items differ from the
// device snapshot — adds, change-number bumps, and deletes — the GetItemEstimate
// counterpart of objectChanges' diff.
func objectChangeCount(snap map[string]int64, objs []objectstore.FolderObject) int {
	count := 0
	live := make(map[string]bool, len(objs))
	for _, o := range objs {
		sid := strconv.FormatInt(o.ID, 10)
		live[sid] = true
		if prev, ok := snap[sid]; !ok || prev != int64(o.ChangeNumber) {
			count++
		}
	}
	for sid := range snap {
		if !live[sid] {
			count++
		}
	}
	return count
}

// holdForSync blocks a hanging Sync until a watched collection changes (a push wake,
// or the fallback cadence catching it) or the heartbeat expires. It returns true
// when changes were found (the caller collects and responds) and false on expiry or
// client disconnect (the caller replies empty). Each change check opens a fresh
// store so a delivery committed by another daemon is observed.
func (s *Server) holdForSync(ctx context.Context, mailbox string, dev *deviceState, collections *wbxml.Node, heartbeat time.Duration) bool {
	var wake <-chan struct{}
	if s.waker != nil {
		ch, cancel := s.waker.Register(mailbox)
		defer cancel()
		wake = ch
	}
	deadline := time.Now().Add(heartbeat)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-wake:
			if s.syncCheck(mailbox, dev, collections) {
				return true
			}
		case <-time.After(min(syncHoldCadence, remaining)):
			if s.syncCheck(mailbox, dev, collections) {
				return true
			}
		}
	}
}

// syncCheck opens a fresh store and reports whether any watched collection changed.
func (s *Server) syncCheck(mailbox string, dev *deviceState, collections *wbxml.Node) bool {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return false
	}
	defer st.Close()
	return syncHasChanges(st, dev, collections)
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
		return syncResponse(collID, cstate.SyncKey, nil, nil, false), nil
	}
	// A key that is not the one we last issued forces a re-prime (Status 3). v1
	// does not replay a dropped response; the client recovers by re-priming.
	if clientKey == "" || clientKey != cstate.SyncKey {
		return syncStatus(collID, "0", syncStatusInvalidKey), nil
	}
	if cstate.Items == nil {
		cstate.Items = map[string]int64{}
	}

	// Object collections (calendar, contacts) take a separate path: their items are
	// read from the object store (never the IMAP index) and versioned by change
	// number, not IMAP flags. The device's Add/Change/Delete edits are applied first
	// (folded into the snapshot so they are not echoed back), then server-side changes
	// are streamed through the folder's data-class renderer.
	if isObjectFolder(folderID) {
		responses := applyObjectClientCommands(st, folderID, cstate, c)
		cmds, more, err := objectChanges(st, folderID, cstate, window, objectAppData(folderID))
		if err != nil {
			return nil, err
		}
		cstate.SyncKey = nextSyncKey(clientKey)
		return syncResponse(collID, cstate.SyncKey, cmds, responses, more), nil
	}

	// Apply the client's commands first, folding each into the snapshot so the
	// diff below does not echo the client's own change back to it.
	applyClientCommands(st, folderID, cstate, c)

	pref := parseBodyPref(c)

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
				wbxml.Str(wbxml.ASServerID, ch.sid), emailAppData(raw, ch.m, collID, ch.sid, pref)))
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
	return syncResponse(collID, cstate.SyncKey, cmds, nil, more), nil
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

// bodyPref is the body representation a Sync request asked for: the chosen
// AirSyncBase body Type (0 = no preference → full MIME) and the truncation size
// the device set for it (0 = untruncated).
type bodyPref struct {
	typ        int
	truncation int
}

// EAS AirSyncBase body types (MS-ASAIRS §2.2.2.22.4).
const (
	bodyTypePlain = 1
	bodyTypeHTML  = 2
	bodyTypeMIME  = 4
)

// parseBodyPref reads a Sync collection's Options/BodyPreference list and selects
// the body type to serve. A device may offer several types; the best match is HTML,
// then MIME, then plain (matching the reference: HTML and plain cost less bandwidth
// than a full MIME message). Only unsupported types (e.g. RTF) or no Options leaves
// typ 0, so the server serves the full MIME as before.
func parseBodyPref(c *wbxml.Node) bodyPref {
	opts := c.Child(wbxml.ASOptions)
	if opts == nil {
		return bodyPref{}
	}
	trunc := map[int]int{}
	for _, ch := range opts.Children {
		if ch.Tag != wbxml.ABBodyPreference {
			continue
		}
		if t, err := strconv.Atoi(ch.ChildText(wbxml.ABType)); err == nil {
			n, _ := strconv.Atoi(ch.ChildText(wbxml.ABTruncationSize))
			trunc[t] = n
		}
	}
	for _, t := range []int{bodyTypeHTML, bodyTypeMIME, bodyTypePlain} {
		if n, ok := trunc[t]; ok {
			return bodyPref{typ: t, truncation: n}
		}
	}
	return bodyPref{}
}

// emailAppData builds the ApplicationData for an Email-class item: the listing
// properties from the index, the message body in the requested representation, and —
// when the message carries attachments — an AirSyncBase Attachments listing whose
// FileReferences the device fetches through ItemOperations.
func emailAppData(raw []byte, m objectstore.MessageInfo, collID, serverID string, pref bodyPref) *wbxml.Node {
	data := wbxml.Elem(wbxml.ASData,
		wbxml.Str(wbxml.EMSubject, m.Subject),
		wbxml.Str(wbxml.EMFrom, m.Sender),
		wbxml.Str(wbxml.EMDateReceived, m.InternalDate.UTC().Format("2006-01-02T15:04:05.000Z")),
		wbxml.Str(wbxml.EMMessageClass, "IPM.Note"),
		wbxml.Str(wbxml.EMRead, readFlag(m.Flags)),
		emailBody(raw, pref),
	)
	// ConversationId/ConversationIndex (MS-ASEMAIL, Since 14.0) group the thread; the
	// id hashes the thread root so every reply shares it, the index carries it with the
	// delivery time for ordering.
	convID := conversationID(raw)
	data.Children = append(data.Children,
		wbxml.Opaque(wbxml.EM2ConversationId, convID),
		wbxml.Opaque(wbxml.EM2ConversationIndex, conversationIndex(convID, m.InternalDate)))
	if atts := attachmentsNode(collID, serverID, messageAttachments(raw)); atts != nil {
		data.Children = append(data.Children, atts)
	}
	return data
}

// emailBody renders the AirSyncBase Body in the device's requested representation
// (MS-ASAIRS §2.2.2.22). Type 0/4 serve the full MIME message; types 1 and 2 extract
// the plain-text or HTML body through the proven MIME→MAPI converter, applying the
// truncation size and marking the body Truncated. An unavailable representation (no
// HTML part, or a conversion failure) falls back to the full MIME.
func emailBody(raw []byte, pref bodyPref) *wbxml.Node {
	mime := func() *wbxml.Node {
		return wbxml.Elem(wbxml.ABBody,
			wbxml.Str(wbxml.ABType, strconv.Itoa(bodyTypeMIME)),
			wbxml.Str(wbxml.ABEstimatedDataSize, strconv.Itoa(len(raw))),
			wbxml.Opaque(wbxml.ABData, raw))
	}
	if pref.typ == 0 || pref.typ == bodyTypeMIME {
		return mime()
	}
	msg, err := oxcmail.Import(raw, oxcmail.Options{})
	if err != nil {
		return mime()
	}
	var content []byte
	if pref.typ == bodyTypeHTML {
		if v, ok := msg.Props.Get(mapi.PrHTML); ok {
			if b, ok := v.([]byte); ok {
				content = b
			}
		}
	}
	if len(content) == 0 { // plain requested, or no HTML part
		if v, ok := msg.Props.Get(mapi.PrBody); ok {
			if s, ok := v.(string); ok {
				content = []byte(s)
			}
		}
		if pref.typ == bodyTypeHTML {
			pref.typ = bodyTypePlain // downgraded: no HTML available
		}
	}
	if content == nil {
		return mime()
	}
	full := len(content)
	fields := []*wbxml.Node{
		wbxml.Str(wbxml.ABType, strconv.Itoa(pref.typ)),
		wbxml.Str(wbxml.ABEstimatedDataSize, strconv.Itoa(full)),
	}
	if pref.truncation > 0 && full > pref.truncation {
		content = content[:pref.truncation]
		fields = append(fields, wbxml.Str(wbxml.ABTruncated, "1"))
	}
	fields = append(fields, wbxml.Opaque(wbxml.ABData, content))
	return wbxml.Elem(wbxml.ABBody, fields...)
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
func syncResponse(collID, key string, cmds, responses []*wbxml.Node, more bool) *wbxml.Node {
	children := []*wbxml.Node{
		wbxml.Str(wbxml.ASSyncKey, key),
		wbxml.Str(wbxml.ASCollectionID, collID),
		wbxml.Str(wbxml.ASStatus, strconv.Itoa(syncStatusOK)),
	}
	// Responses (the outcome of the device's own commands, e.g. an Add's assigned
	// server id) precede the server-to-client Commands (MS-ASCMD 2.2.2.20).
	if len(responses) > 0 {
		children = append(children, wbxml.Elem(wbxml.ASResponses, responses...))
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

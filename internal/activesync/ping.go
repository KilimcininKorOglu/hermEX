package activesync

import (
	"net/http"
	"strconv"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// Ping heartbeat bounds and the poll cadence within a heartbeat.
const (
	minHeartbeat = 1 * time.Second
	maxHeartbeat = 3540 * time.Second
	pingPoll     = 5 * time.Second
)

// Ping status codes (MS-ASCMD 2.2.3.166): 1 the heartbeat expired with no
// change, 2 one or more watched folders changed, 3 the request named no folder
// to watch, 5 the requested heartbeat is out of range, 7 a watched folder has
// not been synced (the device must sync it before Ping can watch it).
const (
	pingStatusExpired      = 1
	pingStatusChanges      = 2
	pingStatusNoFolders    = 3
	pingStatusBadHeartbeat = 5
	pingStatusFolderSync   = 7
)

// handlePing answers Ping: it holds the request open for the heartbeat interval,
// polling the watched folders against the device's last-synced snapshot, and
// returns as soon as a folder changes (Status 2) or when the heartbeat expires
// (Status 1). The change detection reuses the same snapshot diff as Sync.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	folderIDs := pingFolders(root)
	heartbeat, ok := parseHeartbeat(root.ChildText(wbxml.PGHeartbeatInt))
	if !ok {
		// The requested interval is out of range: tell the device the nearest
		// acceptable value (Status 5) so it retries, rather than silently holding
		// for a different interval than it asked for.
		writeWBXML(w, pingOutOfRange(heartbeat))
		return
	}
	if len(folderIDs) == 0 {
		// Nothing to watch: the device must name at least one folder.
		writeWBXML(w, pingResponse(pingStatusNoFolders, nil))
		return
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	state, err := loadState(st)
	st.Close()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dev := state.device(sess.req.deviceID)

	// Ping can only watch a folder the device has already synced (its snapshot is
	// the change baseline). A watched folder with no snapshot means the device
	// must sync first — report Status 7 rather than silently ignoring it.
	for _, id := range folderIDs {
		if dev.Collections[id] == nil {
			writeWBXML(w, pingResponse(pingStatusFolderSync, nil))
			return
		}
	}

	deadline := time.Now().Add(heartbeat)
	for {
		if changed := s.pingCheck(sess.mailbox, dev, folderIDs); len(changed) > 0 {
			writeWBXML(w, pingResponse(pingStatusChanges, changed))
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			writeWBXML(w, pingResponse(pingStatusExpired, nil))
			return
		}
		time.Sleep(min(pingPoll, remaining))
	}
}

// pingCheck reports which watched folders have changes the device has not yet
// synced, by diffing each folder's live state against the device's snapshot. A
// folder with no snapshot (never synced) is skipped — the client must Sync it
// before Ping can watch it.
func (s *Server) pingCheck(mailbox string, dev *deviceState, folderIDs []string) []string {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil
	}
	defer st.Close()
	var changed []string
	for _, collID := range folderIDs {
		cstate := dev.Collections[collID]
		if cstate == nil {
			continue
		}
		folderID, err := strconv.ParseInt(collID, 10, 64)
		if err != nil {
			continue
		}
		live, err := st.ListMessages(folderID)
		if err != nil {
			continue
		}
		if len(diffSnapshot(cstate.Items, live)) > 0 {
			changed = append(changed, collID)
		}
	}
	return changed
}

// pingFolders extracts the watched folder ids from a Ping request.
func pingFolders(root *wbxml.Node) []string {
	folders := root.Child(wbxml.PGFolders)
	if folders == nil {
		return nil
	}
	var out []string
	for _, f := range folders.Children {
		if f.Tag == wbxml.PGFolder {
			if id := f.ChildText(wbxml.PGID); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

// parseHeartbeat parses the requested heartbeat seconds. ok is false when the
// value was present but out of the supported range — the caller replies Status 5
// with the returned nearest bound so the device retries with an acceptable
// interval. A missing or unparseable value defaults to the maximum with ok true.
func parseHeartbeat(s string) (d time.Duration, ok bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return maxHeartbeat, true
	}
	d = time.Duration(n) * time.Second
	if d < minHeartbeat {
		return minHeartbeat, false
	}
	if d > maxHeartbeat {
		return maxHeartbeat, false
	}
	return d, true
}

// pingOutOfRange builds a Status-5 reply naming the nearest acceptable heartbeat
// (in seconds) so the device retries with an in-range interval.
func pingOutOfRange(bound time.Duration) *wbxml.Node {
	return wbxml.Elem(wbxml.PGPing,
		wbxml.Str(wbxml.PGStatus, strconv.Itoa(pingStatusBadHeartbeat)),
		wbxml.Str(wbxml.PGHeartbeatInt, strconv.Itoa(int(bound/time.Second))),
	)
}

// pingResponse builds a Ping reply with the given status and, for Status 2, the
// changed folder ids.
func pingResponse(status int, changed []string) *wbxml.Node {
	children := []*wbxml.Node{wbxml.Str(wbxml.PGStatus, strconv.Itoa(status))}
	if len(changed) > 0 {
		folders := make([]*wbxml.Node, 0, len(changed))
		for _, id := range changed {
			folders = append(folders, wbxml.Str(wbxml.PGFolder, id))
		}
		children = append(children, wbxml.Elem(wbxml.PGFolders, folders...))
	}
	return wbxml.Elem(wbxml.PGPing, children...)
}

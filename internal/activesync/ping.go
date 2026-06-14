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

// Ping status codes (MS-ASCMD): 1 the heartbeat expired with no change, 2 one
// or more watched folders changed.
const (
	pingStatusExpired = 1
	pingStatusChanges = 2
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
	heartbeat := clampHeartbeat(root.ChildText(wbxml.PGHeartbeatInt))

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

// clampHeartbeat parses the requested heartbeat seconds and clamps it to the
// supported range.
func clampHeartbeat(s string) time.Duration {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return maxHeartbeat
	}
	d := time.Duration(n) * time.Second
	if d < minHeartbeat {
		return minHeartbeat
	}
	if d > maxHeartbeat {
		return maxHeartbeat
	}
	return d
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

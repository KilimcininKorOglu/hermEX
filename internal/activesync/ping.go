package activesync

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
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

// Ping status codes (MS-ASCMD 2.2.3.177.11): 1 the heartbeat expired with no
// change, 2 one or more watched folders changed, 3 the request named no folder
// to watch, 4 a syntax error in the request, 5 the requested heartbeat is out of
// range, 7 a watched folder has not been synced (the device must sync it before
// Ping can watch it).
const (
	pingStatusExpired      = 1
	pingStatusChanges      = 2
	pingStatusNoFolders    = 3
	pingStatusSyntax       = 4
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
		s.failRequest(w, r, "wbxml.parse.fail", err, http.StatusBadRequest, "invalid WBXML")
		return
	}
	folderIDs := pingFolders(root)
	heartbeat, refused := parseHeartbeat(root.Child(wbxml.PGHeartbeatInt))
	switch refused {
	case pingStatusBadHeartbeat:
		// Tell the device the nearest acceptable value so it retries, rather than
		// silently holding for a different interval than it asked for.
		writeWBXML(w, pingOutOfRange(heartbeat))
		return
	case pingStatusSyntax:
		writeWBXML(w, pingResponse(pingStatusSyntax, nil))
		return
	}
	if len(folderIDs) == 0 {
		// Nothing to watch: the device must name at least one folder.
		writeWBXML(w, pingResponse(pingStatusNoFolders, nil))
		return
	}

	dev, err := s.pingDevice(sess)
	if err != nil {
		s.failRequest(w, r, "ping.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}

	// Ping can only watch a folder the device has already synced (its snapshot is
	// the change baseline). A watched folder with no snapshot means the device
	// must sync first, report Status 7 rather than silently ignoring it.
	for _, id := range folderIDs {
		if dev.Collections[id] == nil {
			writeWBXML(w, pingResponse(pingStatusFolderSync, nil))
			return
		}
	}
	s.holdPing(w, r, sess, dev, folderIDs, heartbeat)
}

// pingDevice reads the calling device's sync state, closing the store again so a
// held Ping does not keep the mailbox open for its whole heartbeat.
func (s *Server) pingDevice(sess *session) (*deviceState, error) {
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		return nil, err
	}
	state, err := loadState(st)
	_ = st.Close()
	if err != nil {
		return nil, err
	}
	return state.device(sess.req.deviceID), nil
}

// holdPing holds the request open until a watched folder changes (Status 2), the
// heartbeat expires (Status 1), or the device disconnects.
func (s *Server) holdPing(w http.ResponseWriter, r *http.Request, sess *session,
	dev *deviceState, folderIDs []string, heartbeat time.Duration) {
	deadline := time.Now().Add(heartbeat)
	// Register the mailbox for a push wake before the first poll, so a change landing
	// during the heartbeat wakes Ping at once. A nil waker (push disabled) leaves wake
	// nil, and the loop runs on its cadence exactly as before.
	var wake <-chan struct{}
	if s.waker != nil {
		ch, cancel := s.waker.Register(sess.mailbox)
		defer cancel()
		wake = ch
	}
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
		select {
		case <-r.Context().Done():
			return // device disconnected; nothing to write
		case <-wake:
			// a push wake, loop and pingCheck observes the change
		case <-time.After(min(pingPoll, remaining)):
		}
		// Keep the long-held Ping visible in the live-session monitor.
		s.touchSession(sess)
	}
}

// pingCheck reports which watched folders have changes the device has not yet
// synced, by diffing each folder's live state against the device's snapshot. A
// folder with no snapshot (never synced) is skipped, the client must Sync it
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

// parseHeartbeat reads the requested heartbeat. It answers the interval to hold
// and 0, or the interval to report and the Ping status refusing the request: a
// value that is not a decimal integer is a syntax error (Status 4), and one
// outside minHeartbeat..maxHeartbeat is Status 5 with the nearest bound
// (MS-ASCMD 2.2.3.177.11). The range is compared in whole seconds before any
// Duration is built, so a huge value cannot wrap into the range. An absent
// element holds for the maximum.
func parseHeartbeat(el *wbxml.Node) (time.Duration, int) {
	if el == nil {
		return maxHeartbeat, 0
	}
	n, err := strconv.ParseInt(el.Text, 10, 64)
	switch {
	case errors.Is(err, strconv.ErrRange):
		return heartbeatBound(strings.HasPrefix(el.Text, "-")), pingStatusBadHeartbeat
	case err != nil:
		return 0, pingStatusSyntax
	case n < int64(minHeartbeat/time.Second):
		return minHeartbeat, pingStatusBadHeartbeat
	case n > int64(maxHeartbeat/time.Second):
		return maxHeartbeat, pingStatusBadHeartbeat
	}
	return time.Duration(n) * time.Second, 0
}

// heartbeatBound is the bound a heartbeat too large to parse is held to: the
// shortest interval for a negative one, the longest otherwise.
func heartbeatBound(negative bool) time.Duration {
	if negative {
		return minHeartbeat
	}
	return maxHeartbeat
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

package activesync

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/serve"
	"hermex/internal/wbxml"
)

// Ping heartbeat bounds and the poll cadence within a heartbeat.
const (
	// minHeartbeat matches the Sync Wait/HeartbeatInterval floor (MS-ASCMD, 60
	// to 3540 seconds); Ping leaves its range to the server, and the operator
	// chose the same floor so a device cannot poll every second.
	minHeartbeat = 60 * time.Second
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
	root, err := readPingBody(r)
	if err != nil {
		s.failRequest(w, r, "wbxml.parse.fail", err, http.StatusBadRequest, "invalid WBXML")
		return
	}
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
	dev, p, status, err := s.pingDevice(sess, pingParams{heartbeat: heartbeat, folders: pingFolders(root)})
	if err != nil {
		s.failRequest(w, r, "ping.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}
	if status != 0 {
		writeWBXML(w, pingResponse(status, nil))
		return
	}
	s.holdPing(w, r, sess, dev, p.folders, p.heartbeat)
}

// readPingBody reads a Ping request. An empty body is a legal Ping that reuses
// the cached parameters (MS-ASCMD 3.1.5.8), so it reads as a Ping with no
// children.
func readPingBody(r *http.Request) (*wbxml.Node, error) {
	body, err := serve.ReadBody(r.Body, maxBodyLimit())
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return wbxml.Elem(wbxml.PGPing), nil
	}
	return wbxml.Unmarshal(body)
}

// pingParams is what a Ping watches: the heartbeat and the folder ids. A zero
// heartbeat or an empty folder list means the request did not send it.
type pingParams struct {
	heartbeat time.Duration
	folders   []string
}

// pingDevice reads the calling device's sync state and resolves the parameters
// the Ping runs with. MS-ASCMD 3.1.5.8: the server MUST cache the heartbeat and
// the folders, and use them when a later Ping omits either. A parameter neither
// sent nor cached is Status 3; a folder the device has not synced is Status 7,
// because its snapshot is the change baseline. Only parameters that pass both
// checks are cached, so the stored list holds synced collections only. The store
// is closed again so a held Ping does not keep the mailbox open.
func (s *Server) pingDevice(sess *session, sent pingParams) (*deviceState, pingParams, int, error) {
	st, err := objectstore.OpenExisting(sess.mailbox)
	if err != nil {
		return nil, pingParams{}, 0, err
	}
	defer s.closeStore(sess.user, st)
	state, err := loadState(st)
	if err != nil {
		return nil, pingParams{}, 0, err
	}
	dev := state.device(sess.req.deviceID)
	p := dev.pingParams(sent)
	if p.heartbeat == 0 || len(p.folders) == 0 {
		return nil, p, pingStatusNoFolders, nil
	}
	for _, id := range p.folders {
		if dev.Collections[id] == nil {
			return nil, p, pingStatusFolderSync, nil
		}
	}
	err = updateState(st, func(s *asState) bool { return s.device(sess.req.deviceID).cachePing(p) })
	return dev, p, 0, err
}

// pingParams fills what a Ping did not send from the device's cache. A cached
// heartbeat is held to the current bounds, since it may predate them.
func (d *deviceState) pingParams(sent pingParams) pingParams {
	if sent.heartbeat == 0 && d.PingHeartbeat > 0 {
		sent.heartbeat = min(max(time.Duration(d.PingHeartbeat)*time.Second, minHeartbeat), maxHeartbeat)
	}
	if len(sent.folders) == 0 {
		sent.folders = d.PingFolders
	}
	return sent
}

// cachePing records the parameters a Ping runs with, reporting whether they
// changed and the state needs saving.
func (d *deviceState) cachePing(p pingParams) bool {
	secs := int64(p.heartbeat / time.Second)
	if d.PingHeartbeat == secs && slices.Equal(d.PingFolders, p.folders) {
		return false
	}
	d.PingHeartbeat, d.PingFolders = secs, p.folders
	return true
}

// holdPing holds the request open until a watched folder changes (Status 2), the
// heartbeat expires (Status 1), or the device disconnects.
func (s *Server) holdPing(w http.ResponseWriter, r *http.Request, sess *session,
	dev *deviceState, folderIDs []string, heartbeat time.Duration) {
	if s.pingHold > 0 {
		heartbeat = s.pingHold
	}
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
	st, err := objectstore.OpenExisting(mailbox)
	if err != nil {
		return nil
	}
	// The check has only the mailbox path; the log names no user.
	defer s.closeStore("", st)
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
		if folderDiffers(st, folderID, cstate) {
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
// element answers 0 and 0: the cached heartbeat applies.
func parseHeartbeat(el *wbxml.Node) (time.Duration, int) {
	if el == nil {
		return 0, 0
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

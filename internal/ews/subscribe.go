package ews

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"net/http"
	"sync"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// --- registry types ---

// ewsSubscription is one server-side notification subscription (MS-OXWSNTIF). It
// holds the baseline-at-registration snapshot the next GetEvents diffs against;
// mu guards that snapshot so two concurrent polls cannot double-drain or race the
// advance.
type ewsSubscription struct {
	mu         sync.Mutex
	user       string
	mailbox    string
	streaming  bool       // a streaming sub has no GetEvents consumer (served by 2b)
	allFolders bool       // whole-store (folder set re-enumerated each poll)
	folderIDs  []int64    // explicit subscribed folders (nil when allFolders)
	want       eventWants // the producible event types the client subscribed to
	created    time.Time
	timeout    time.Duration              // from the SubscriptionId
	snap       map[int64]map[int64]uint64 // folderID → (messageID → change number)
}

// eventWants records which producible event types a subscription wants. The poll
// diff produces only Created/Deleted/Modified; Moved/Copied/NewMail/FreeBusy are
// accepted in the request but never produced — a poll cannot distinguish a move
// or copy from a create/delete, and new-mail is delivery-triggered — a documented
// gap shared with the ROP notification path.
type eventWants struct {
	created, deleted, modified bool
}

// subError is an EWS per-message error code returned by GetEvents. It is distinct
// from an internal error (which becomes a SOAP fault): the reference catches these
// locally and reports them as a ResponseMessage with ResponseClass="Error".
type subError string

func (e subError) Error() string { return string(e) }

const (
	errSubInvalid subError = "ErrorInvalidSubscription"
	errSubAccess  subError = "ErrorAccessDenied"
)

// --- request types ---

type subscribeRequest struct {
	Pull      *subscriptionReq `xml:"PullSubscriptionRequest"`
	Streaming *subscriptionReq `xml:"StreamingSubscriptionRequest"`
	// PushSubscriptionRequest is intentionally not parsed: hermEX serves no
	// outbound push callback, so a push Subscribe falls through to the
	// unsupported-type fault below.
}

type subscriptionReq struct {
	SubscribeToAllFolders bool       `xml:"SubscribeToAllFolders,attr"`
	FolderIDs             folderRefs `xml:"FolderIds"`
	EventTypes            struct {
		Types []string `xml:"EventType"`
	} `xml:"EventTypes"`
	Timeout int `xml:"Timeout"` // pull only, minutes
}

type unsubscribeRequest struct {
	SubscriptionID string `xml:"SubscriptionId"`
}

type getEventsRequest struct {
	SubscriptionID string `xml:"SubscriptionId"`
	// Watermark is accepted on the wire but ignored (the reference does not
	// implement watermarks; subscription identity rides in the SubscriptionId).
}

// --- response types ---

type subscribeResponse struct {
	XMLName  xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SubscribeResponse"`
	Messages []subscribeResponseMessage `xml:"ResponseMessages>SubscribeResponseMessage"`
}

type subscribeResponseMessage struct {
	ResponseClass  string `xml:"ResponseClass,attr"`
	ResponseCode   string `xml:"ResponseCode"`
	SubscriptionID string `xml:"SubscriptionId,omitempty"`
}

type unsubscribeResponse struct {
	XMLName  xml.Name               `xml:"http://schemas.microsoft.com/exchange/services/2006/messages UnsubscribeResponse"`
	Messages []availResponseMessage `xml:"ResponseMessages>UnsubscribeResponseMessage"`
}

type getEventsResponse struct {
	XMLName  xml.Name                   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetEventsResponse"`
	Messages []getEventsResponseMessage `xml:"ResponseMessages>GetEventsResponseMessage"`
}

type getEventsResponseMessage struct {
	ResponseClass string        `xml:"ResponseClass,attr"`
	ResponseCode  string        `xml:"ResponseCode"`
	Notification  *notification `xml:"Notification,omitempty"`
}

// notification is the <m:Notification> payload. Its leaves switch to the types
// namespace; each event element carries its own types-namespace name.
type notification struct {
	SubscriptionID string `xml:"http://schemas.microsoft.com/exchange/services/2006/types SubscriptionId"`
	Events         []notifEvent
}

// notifEvent is one change event. The element name (CreatedEvent/DeletedEvent/
// ModifiedEvent/StatusEvent) is set per-instance via XMLName, carrying the types
// namespace; the children inherit it. A StatusEvent has no children (the empty
// heartbeat emitted when the queue is empty).
type notifEvent struct {
	XMLName   xml.Name
	TimeStamp string `xml:"TimeStamp,omitempty"`
	ItemID    *refID `xml:"ItemId,omitempty"`
	Parent    *refID `xml:"ParentFolderId,omitempty"`
}

// --- handlers ---

// handleSubscribe answers Subscribe (MS-OXWSNTIF): it registers a pull or
// streaming subscription over the requested folders (or the whole mailbox),
// taking a baseline snapshot so the first GetEvents reports only changes since
// the subscription was created. A malformed request faults at the envelope level
// (ErrorInvalidSubscriptionRequest), matching the reference's no-local-catch path.
func (s *Server) handleSubscribe(w http.ResponseWriter, inner []byte, sess *session) {
	var req subscribeRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "Subscribe: "+err.Error())
		return
	}
	sub, streaming := req.Pull, false
	if sub == nil {
		sub, streaming = req.Streaming, true
	}
	if sub == nil {
		writeSOAPFault(w, "ErrorInvalidSubscriptionRequest", "Subscribe: unsupported subscription type")
		return
	}

	all := sub.SubscribeToAllFolders
	targets := resolveTargets(sub.FolderIDs)
	if all && len(targets) > 0 {
		writeSOAPFault(w, "ErrorInvalidSubscriptionRequest", "Subscribe: SubscribeToAllFolders with explicit FolderIds")
		return
	}
	var folderIDs []int64
	for _, t := range targets {
		if !t.ok {
			writeSOAPFault(w, "ErrorInvalidSubscriptionRequest", "Subscribe: unresolvable folder id")
			return
		}
		folderIDs = append(folderIDs, t.fid)
	}
	allFolders := all || len(folderIDs) == 0

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	snap, err := snapshotFolders(st, allFolders, folderIDs)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}

	timeoutMin := sub.Timeout
	if timeoutMin <= 0 {
		timeoutMin = 30 // the reference's default pull timeout
	}
	id := s.registerSubscription(sess, streaming, allFolders, folderIDs, parseEventWants(sub.EventTypes.Types), timeoutMin, snap)

	writeResponse(w, subscribeResponse{Messages: []subscribeResponseMessage{{
		ResponseClass: "Success", ResponseCode: "NoError", SubscriptionID: id,
	}}})
}

// handleUnsubscribe answers Unsubscribe: it drops the subscription, reporting
// ErrorSubscriptionNotFound (per-message, not a fault) when it does not exist or
// belongs to another user.
func (s *Server) handleUnsubscribe(w http.ResponseWriter, inner []byte, sess *session) {
	var req unsubscribeRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "Unsubscribe: "+err.Error())
		return
	}
	msg := availResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}
	if !s.removeSubscription(req.SubscriptionID, sess.user) {
		msg = availResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorSubscriptionNotFound"}
	}
	writeResponse(w, unsubscribeResponse{Messages: []availResponseMessage{msg}})
}

// handleGetEvents answers GetEvents (pull): it polls the subscribed folders,
// diffs against the subscription's snapshot, and returns the change events. A bad
// or expired subscription, or a cross-user access, is a per-message error
// (ErrorInvalidSubscription/ErrorAccessDenied); a store failure is a SOAP fault.
func (s *Server) handleGetEvents(w http.ResponseWriter, inner []byte, sess *session) {
	var req getEventsRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetEvents: "+err.Error())
		return
	}
	msg, err := s.getEvents(req.SubscriptionID, sess.user)
	if err != nil {
		if se, ok := errors.AsType[subError](err); ok {
			writeResponse(w, getEventsResponse{Messages: []getEventsResponseMessage{{
				ResponseClass: "Error", ResponseCode: string(se),
			}}})
			return
		}
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	writeResponse(w, getEventsResponse{Messages: []getEventsResponseMessage{msg}})
}

// --- registry + poll-diff ---

// registerSubscription stores a new subscription and returns its SubscriptionId.
func (s *Server) registerSubscription(sess *session, streaming, allFolders bool, folderIDs []int64, want eventWants, timeoutMin int, snap map[int64]map[int64]uint64) string {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.subSeq++
	id := encodeSubscriptionID(s.subSeq, uint32(timeoutMin))
	s.subs[id] = &ewsSubscription{
		user:       sess.user,
		mailbox:    sess.mailbox,
		streaming:  streaming,
		allFolders: allFolders,
		folderIDs:  folderIDs,
		want:       want,
		created:    time.Now(),
		timeout:    time.Duration(timeoutMin) * time.Minute,
		snap:       snap,
	}
	return id
}

// removeSubscription drops a subscription, reporting false when it is absent or
// owned by another user (both surface as ErrorSubscriptionNotFound).
func (s *Server) removeSubscription(id, user string) bool {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	sub, ok := s.subs[id]
	if !ok || sub.user != user {
		return false
	}
	delete(s.subs, id)
	return true
}

// getEvents looks up a subscription, polls it, and builds the GetEvents response
// message. An expired subscription is evicted and reported as invalid.
func (s *Server) getEvents(id, user string) (getEventsResponseMessage, error) {
	s.subMu.Lock()
	sub, ok := s.subs[id]
	if ok && time.Since(sub.created) > sub.timeout {
		delete(s.subs, id)
		ok = false
	}
	s.subMu.Unlock()
	if !ok {
		return getEventsResponseMessage{}, errSubInvalid
	}
	if sub.user != user {
		return getEventsResponseMessage{}, errSubAccess
	}

	// Hold the per-subscription lock across read-snapshot → diff → advance so two
	// concurrent GetEvents cannot double-drain or race the snapshot advance.
	sub.mu.Lock()
	defer sub.mu.Unlock()

	st, err := objectstore.Open(sub.mailbox)
	if err != nil {
		return getEventsResponseMessage{}, err
	}
	defer st.Close()

	events, err := pollSubscription(st, sub)
	if err != nil {
		return getEventsResponseMessage{}, err
	}
	notif := &notification{SubscriptionID: id, Events: events}
	if len(events) == 0 {
		// An empty queue still carries a single StatusEvent (the heartbeat).
		notif.Events = []notifEvent{statusEvent()}
	}
	return getEventsResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", Notification: notif}, nil
}

// snapshotFolders captures the message change-number map of every in-scope folder
// — the baseline a subsequent poll diffs against.
func snapshotFolders(st *objectstore.Store, allFolders bool, folderIDs []int64) (map[int64]map[int64]uint64, error) {
	folders, err := scopeFolders(st, allFolders, folderIDs)
	if err != nil {
		return nil, err
	}
	snap := make(map[int64]map[int64]uint64, len(folders))
	for _, fid := range folders {
		cns, err := st.FolderMessageChangeNumbers(fid)
		if err != nil {
			return nil, err
		}
		snap[fid] = cns
	}
	return snap, nil
}

// scopeFolders resolves the folder set to poll: the explicit list, or — for a
// whole-store subscription — the mailbox's current folders, re-enumerated each
// call so a folder created after the subscription is included.
func scopeFolders(st *objectstore.Store, allFolders bool, folderIDs []int64) ([]int64, error) {
	if !allFolders {
		return folderIDs, nil
	}
	all, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(all))
	for _, f := range all {
		out = append(out, f.ID)
	}
	return out, nil
}

// pollSubscription diffs each in-scope folder's current message change numbers
// against the subscription snapshot, emits the wanted events, and advances the
// snapshot. A new message id is a create, a vanished id a delete, and a changed
// change number a modify (FolderMessageChangeNumbers folds read_cn in, so a
// \Seen flip surfaces as a modify).
func pollSubscription(st *objectstore.Store, sub *ewsSubscription) ([]notifEvent, error) {
	folders, err := scopeFolders(st, sub.allFolders, sub.folderIDs)
	if err != nil {
		return nil, err
	}
	now := timeStampNow()
	var events []notifEvent
	present := make(map[int64]bool, len(folders))
	for _, fid := range folders {
		present[fid] = true
		cur, err := st.FolderMessageChangeNumbers(fid)
		if err != nil {
			return nil, err
		}
		prev := sub.snap[fid] // nil for a folder new since the baseline → its messages are creates
		for mid, cn := range cur {
			pcn, existed := prev[mid]
			switch {
			case !existed && sub.want.created:
				events = append(events, messageEvent(st, "CreatedEvent", fid, mid, now))
			case existed && cn != pcn && sub.want.modified:
				events = append(events, messageEvent(st, "ModifiedEvent", fid, mid, now))
			}
		}
		if sub.want.deleted {
			for mid := range prev {
				if _, still := cur[mid]; !still {
					events = append(events, messageEvent(st, "DeletedEvent", fid, mid, now))
				}
			}
		}
		sub.snap[fid] = cur
	}
	// Prune snapshot entries for folders no longer in scope (a deleted folder for a
	// whole-store sub) so the map does not grow unbounded; folder-hierarchy events
	// themselves are a deferred increment.
	if sub.allFolders {
		for fid := range sub.snap {
			if !present[fid] {
				delete(sub.snap, fid)
			}
		}
	}
	return events, nil
}

// messageEvent builds one message change event. The item id resolves the message
// to its (folder, uid) so a follow-up GetItem works; a non-indexed item (calendar/
// contact) has no uid, leaving a correlation-only id.
func messageEvent(st *objectstore.Store, kind string, folderID, messageID int64, timeStamp string) notifEvent {
	uid, _, _ := st.MessageUIDByID(folderID, messageID)
	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: folderID, MessageID: messageID, UID: uid})
	return notifEvent{
		XMLName:   xml.Name{Space: nsTypes, Local: kind},
		TimeStamp: timeStamp,
		ItemID:    &refID{ID: itemID},
		Parent:    &refID{ID: oxews.EncodeFolderID(folderID)},
	}
}

// parseEventWants maps the requested EventType names to the producible set; the
// non-producible types (Moved/Copied/NewMail/FreeBusy) are accepted but ignored.
func parseEventWants(types []string) eventWants {
	var w eventWants
	for _, t := range types {
		switch t {
		case "CreatedEvent":
			w.created = true
		case "DeletedEvent":
			w.deleted = true
		case "ModifiedEvent":
			w.modified = true
		}
	}
	return w
}

// timeStampNow renders the current time as the event TimeStamp (UTC, like the
// reference, which stamps every event with the poll time).
func timeStampNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// encodeSubscriptionID encodes the registry key and timeout (minutes) as the
// reference's 12-char base64 of 8 little-endian bytes [key | timeout-minutes].
func encodeSubscriptionID(key, timeoutMin uint32) string {
	var b [8]byte
	binary.LittleEndian.PutUint32(b[0:4], key)
	binary.LittleEndian.PutUint32(b[4:8], timeoutMin)
	return base64.StdEncoding.EncodeToString(b[:])
}

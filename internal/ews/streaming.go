package ews

import (
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// Streaming-notification constants. The cadence and connection-timeout bounds
// match MS-OXWSNTIF; tests override the cadence and lifetime via Server fields.
const (
	defaultStreamInterval = 45 * time.Second // the reference's continuation cadence
	minConnectionTimeout  = 1                // minutes (Types.xsd bound)
	maxConnectionTimeout  = 30               // minutes (Types.xsd bound)
)

// soapEnvelopeOpenNoDecl is the envelope prologue without the XML declaration:
// streaming continuation chunks are separate documents concatenated into one open
// response, and a mid-stream declaration is not emitted (only the first chunk
// carries it).
var soapEnvelopeOpenNoDecl = strings.TrimPrefix(soapEnvelopeOpen, xml.Header)

// --- request/response types ---

type getStreamingEventsRequest struct {
	SubscriptionIDs   []string `xml:"SubscriptionIds>SubscriptionId"`
	ConnectionTimeout int      `xml:"ConnectionTimeout"` // minutes (1..30)
}

type getStreamingEventsResponse struct {
	XMLName  xml.Name                            `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetStreamingEventsResponse"`
	Messages []getStreamingEventsResponseMessage `xml:"ResponseMessages>GetStreamingEventsResponseMessage"`
}

type getStreamingEventsResponseMessage struct {
	ResponseClass    string         `xml:"ResponseClass,attr"`
	ResponseCode     string         `xml:"ResponseCode"`
	Notifications    []notification `xml:"Notifications>Notification,omitempty"`
	ErrorSubs        *errorSubs     `xml:"ErrorSubscriptionIds,omitempty"`
	ConnectionStatus string         `xml:"ConnectionStatus,omitempty"`
}

type errorSubs struct {
	IDs []string `xml:"http://schemas.microsoft.com/exchange/services/2006/types SubscriptionId"`
}

// --- handler ---

// handleGetStreamingEvents answers GetStreamingEvents (MS-OXWSNTIF streaming): it
// holds the connection open and writes a sequence of GetStreamingEventsResponse
// envelopes — an initial one carrying ConnectionStatus=OK (and any invalid
// subscription ids), then a continuation every interval with the polled events
// (or a StatusEvent heartbeat when idle), then a final one with
// ConnectionStatus=Closed when the connection timeout expires. The response is
// chunked (no Content-Length), which the gateway forwards incrementally.
func (s *Server) handleGetStreamingEvents(w http.ResponseWriter, r *http.Request, inner []byte, sess *session) {
	var req getStreamingEventsRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetStreamingEvents: "+err.Error())
		return
	}

	// Validate each subscription (existence + owner), evicting any that expired —
	// the entry sweep also reclaims streaming subscriptions created but never
	// otherwise accessed.
	var valid, bad []string
	for _, id := range req.SubscriptionIDs {
		if s.streamSubValid(id, sess.user) {
			valid = append(valid, id)
		} else {
			bad = append(bad, id)
		}
	}

	// Resolve cadence and lifetime, mapping the zero values to production defaults
	// before the ticker is built (a zero interval would panic NewTicker).
	interval := s.streamInterval
	if interval <= 0 {
		interval = defaultStreamInterval
	}
	window := s.streamWindow
	if window <= 0 {
		mins := min(max(req.ConnectionTimeout, minConnectionTimeout), maxConnectionTimeout)
		window = time.Duration(mins) * time.Minute
	}

	// Headers freeze on the first write; set the content type and leave the length
	// unset so the response is chunked.
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	rc := http.NewResponseController(w)

	initMsg := getStreamingEventsResponseMessage{
		ResponseClass: "Success", ResponseCode: "NoError", ConnectionStatus: "OK",
	}
	if len(bad) > 0 {
		initMsg.ResponseClass = "Error"
		initMsg.ResponseCode = "ErrorInvalidSubscription"
		initMsg.ErrorSubs = &errorSubs{IDs: bad}
	}
	if !writeStreamChunk(w, rc, streamEnvelope(initMsg), true) {
		return
	}

	// No live subscription → close immediately rather than hold an idle connection.
	if len(valid) == 0 {
		writeStreamChunk(w, rc, streamEnvelope(closedMessage()), false)
		return
	}

	// Poll once immediately so a change between Subscribe and this call lands in the
	// first continuation, then continue on the interval until the window expires or
	// the client disconnects.
	deadline := time.Now().Add(window)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		if !writeStreamChunk(w, rc, s.streamNotifications(valid), false) {
			return // client gone (write/flush failed)
		}
		if !time.Now().Before(deadline) {
			writeStreamChunk(w, rc, streamEnvelope(closedMessage()), false)
			return
		}
		select {
		case <-ctx.Done():
			return // client disconnected: no Closed chunk
		case <-ticker.C:
		}
	}
}

// --- helpers ---

// streamSubValid reports whether a subscription exists and belongs to the user,
// evicting it first if it has expired (the lazy-expiry sweep, run on entry).
func (s *Server) streamSubValid(id, user string) bool {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	sub, ok := s.subs[id]
	if ok && time.Since(sub.created) > sub.timeout {
		delete(s.subs, id)
		ok = false
	}
	return ok && sub.user == user
}

// streamNotifications polls every still-live subscription and builds one
// continuation response, a Notification per subscription (its events, or a
// StatusEvent heartbeat when idle).
func (s *Server) streamNotifications(ids []string) getStreamingEventsResponse {
	var notifs []notification
	for _, id := range ids {
		s.subMu.Lock()
		sub := s.subs[id]
		s.subMu.Unlock()
		if sub == nil {
			continue // unsubscribed mid-stream
		}
		notifs = append(notifs, pollOneForStream(id, sub))
	}
	return getStreamingEventsResponse{Messages: []getStreamingEventsResponseMessage{{
		ResponseClass: "Success", ResponseCode: "NoError",
		Notifications: notifs, ConnectionStatus: "OK",
	}}}
}

// pollOneForStream polls one subscription under its lock and returns its
// Notification (a StatusEvent heartbeat when there is nothing new or the store
// cannot be opened).
func pollOneForStream(id string, sub *ewsSubscription) notification {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	st, err := objectstore.Open(sub.mailbox)
	if err != nil {
		return notification{SubscriptionID: id, Events: []notifEvent{statusEvent()}}
	}
	defer st.Close()
	events, err := pollSubscription(st, sub)
	if err != nil || len(events) == 0 {
		return notification{SubscriptionID: id, Events: []notifEvent{statusEvent()}}
	}
	return notification{SubscriptionID: id, Events: events}
}

// streamEnvelope wraps a single response message in a streaming response.
func streamEnvelope(msg getStreamingEventsResponseMessage) getStreamingEventsResponse {
	return getStreamingEventsResponse{Messages: []getStreamingEventsResponseMessage{msg}}
}

// closedMessage is the final response message: ConnectionStatus=Closed.
func closedMessage() getStreamingEventsResponseMessage {
	return getStreamingEventsResponseMessage{
		ResponseClass: "Success", ResponseCode: "NoError", ConnectionStatus: "Closed",
	}
}

// statusEvent is the empty heartbeat event emitted when a subscription's queue is
// empty.
func statusEvent() notifEvent {
	return notifEvent{XMLName: xml.Name{Space: nsTypes, Local: "StatusEvent"}}
}

// writeStreamChunk marshals one response, writes it as a SOAP envelope (the XML
// declaration only on the first chunk), and flushes. It returns false when any
// write or the flush fails — the signal that the client has gone.
func writeStreamChunk(w http.ResponseWriter, rc *http.ResponseController, resp getStreamingEventsResponse, withDecl bool) bool {
	body, err := xml.Marshal(resp)
	if err != nil {
		return false
	}
	if withDecl {
		if _, err := io.WriteString(w, xml.Header); err != nil {
			return false
		}
	}
	if _, err := io.WriteString(w, soapEnvelopeOpenNoDecl); err != nil {
		return false
	}
	if _, err := w.Write(body); err != nil {
		return false
	}
	if _, err := io.WriteString(w, soapEnvelopeClose); err != nil {
		return false
	}
	return rc.Flush() == nil
}

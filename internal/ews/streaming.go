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
// envelopes, an initial one carrying ConnectionStatus=OK, then a continuation
// every interval with the polled events (or a StatusEvent heartbeat when idle),
// then a final one carrying ConnectionStatus=Closed when the connection timeout
// expires. The response is chunked (no Content-Length), which the gateway
// forwards incrementally.
//
// A subscription id the server will not stream fails the whole call: the response
// is a single ErrorInvalidSubscription message naming it, with
// ConnectionStatus=Closed, and nothing is streamed.
func (s *Server) handleGetStreamingEvents(w http.ResponseWriter, r *http.Request, inner []byte, sess *session) {
	var req getStreamingEventsRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "GetStreamingEvents: invalid request", err)
		return
	}

	valid, bad := s.partitionSubscriptions(req.SubscriptionIDs, sess.user)
	interval, window := s.streamCadence(req.ConnectionTimeout)

	// Headers freeze on the first write; set the content type and leave the length
	// unset so the response is chunked.
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	rc := http.NewResponseController(w)

	if len(bad) > 0 {
		writeStreamChunk(w, rc, streamEnvelope(invalidSubscriptions(bad)), true)
		return
	}
	if !writeStreamChunk(w, rc, streamEnvelope(openingMessage()), true) {
		return
	}

	// No subscription named → close immediately rather than hold an idle connection.
	if len(valid) == 0 {
		writeStreamChunk(w, rc, streamEnvelope(closedMessage()), false)
		return
	}

	s.streamUntilClosed(w, r, rc, valid, interval, window)
}

// partitionSubscriptions validates each subscription (existence + owner),
// evicting any that expired. The entry sweep also reclaims streaming
// subscriptions created but never otherwise accessed.
func (s *Server) partitionSubscriptions(ids []string, user string) (valid, bad []string) {
	for _, id := range ids {
		if s.streamSubValid(id, user) {
			valid = append(valid, id)
		} else {
			bad = append(bad, id)
		}
	}
	return valid, bad
}

// streamUntilClosed polls once immediately, so a change between Subscribe and this
// call lands in the first continuation, then continues on the interval until the
// window expires or the client disconnects.
func (s *Server) streamUntilClosed(w http.ResponseWriter, r *http.Request, rc *http.ResponseController,
	valid []string, interval, window time.Duration) {
	deadline := time.Now().Add(window)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Register a push wake for each watched mailbox so a change emits a continuation
	// at once instead of on the interval. A nil waker (push disabled) leaves wake nil
	// and the loop runs on its ticker exactly as before.
	var wake <-chan struct{}
	if s.waker != nil {
		ch, cancel := s.streamWakes(valid)
		defer cancel()
		wake = ch
	}
	ctx := r.Context()
	for {
		msg, live := s.streamChunk(valid)
		valid = live
		// The connection is done when the window has passed or nothing is left to
		// watch. Reporting it on this chunk puts the closing signal on the same
		// message as the fault that caused it.
		closing := len(valid) == 0 || !time.Now().Before(deadline)
		msg.ConnectionStatus = "OK"
		if closing {
			msg.ConnectionStatus = "Closed"
		}
		if !writeStreamChunk(w, rc, streamEnvelope(msg), false) {
			return // client gone (write/flush failed)
		}
		if closing {
			return
		}
		select {
		case <-ctx.Done():
			return // client disconnected: no Closed chunk
		case <-wake:
			// a push wake, loop and streamChunk emits the change
		case <-ticker.C:
		}
	}
}

// streamCadence resolves the poll interval and the connection lifetime, mapping
// the zero values to production defaults before the ticker is built (a zero
// interval would panic NewTicker).
func (s *Server) streamCadence(connectionTimeout int) (interval, window time.Duration) {
	interval = s.streamInterval
	if interval <= 0 {
		interval = defaultStreamInterval
	}
	window = s.streamWindow
	if window <= 0 {
		mins := min(max(connectionTimeout, minConnectionTimeout), maxConnectionTimeout)
		window = time.Duration(mins) * time.Minute
	}
	return interval, window
}

// openingMessage builds the first chunk's response message for a request whose
// every subscription is live: the stream is open.
func openingMessage() getStreamingEventsResponseMessage {
	return getStreamingEventsResponseMessage{
		ResponseClass: "Success", ResponseCode: "NoError", ConnectionStatus: "OK",
	}
}

// invalidSubscriptions builds the refusal a request naming a subscription this
// server will not stream receives: the ids in ErrorSubscriptionIds and
// ConnectionStatus=Closed, because a subscription that is gone never becomes
// valid again and the client must subscribe anew.
func invalidSubscriptions(bad []string) getStreamingEventsResponseMessage {
	return getStreamingEventsResponseMessage{
		ResponseClass:    "Error",
		ResponseCode:     "ErrorInvalidSubscription",
		ErrorSubs:        &errorSubs{IDs: bad},
		ConnectionStatus: "Closed",
	}
}

// streamWakes registers a push wake for each distinct mailbox among the given
// subscriptions and merges them into one channel, so any watched mailbox changing
// wakes the streaming loop. The returned cancel stops the forwarders and drops the
// registrations; the caller defers it.
func (s *Server) streamWakes(ids []string) (<-chan struct{}, func()) {
	merged := make(chan struct{}, 1)
	done := make(chan struct{})
	seen := make(map[string]bool)
	var cancels []func()
	for _, id := range ids {
		s.subMu.Lock()
		sub := s.subs[id]
		s.subMu.Unlock()
		if sub == nil || seen[sub.mailbox] {
			continue
		}
		seen[sub.mailbox] = true
		ch, cancel := s.waker.Register(sub.mailbox)
		cancels = append(cancels, cancel)
		go func(ch <-chan struct{}) {
			for {
				select {
				case <-done:
					return
				case <-ch:
					select {
					case merged <- struct{}{}:
					default:
					}
				}
			}
		}(ch)
	}
	return merged, func() {
		close(done)
		for _, c := range cancels {
			c()
		}
	}
}

// --- helpers ---

// liveSub returns the subscription at an id, or nil when it is gone. An entry
// whose lifetime has passed is evicted here and reported gone, so the expiry rule
// does not depend on the periodic sweep having run.
func (s *Server) liveSub(id string) *ewsSubscription {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	sub, ok := s.subs[id]
	if !ok {
		return nil
	}
	if time.Since(sub.created) > sub.timeout {
		delete(s.subs, id)
		return nil
	}
	return sub
}

// streamSubValid reports whether a subscription is live and belongs to the user.
func (s *Server) streamSubValid(id, user string) bool {
	sub := s.liveSub(id)
	return sub != nil && sub.user == user
}

// streamChunk polls every still-live subscription and builds one continuation
// message, a Notification per subscription (its events, or a StatusEvent
// heartbeat when idle). It also returns the ids that are still live: one that
// disappeared mid-stream (an Unsubscribe, or its lifetime passing) is named in
// ErrorSubscriptionIds on this same chunk and dropped from the watched set, so
// the client learns of the fault where it happens rather than from a later
// message carrying no detail.
func (s *Server) streamChunk(ids []string) (getStreamingEventsResponseMessage, []string) {
	var notifs []notification
	var gone []string
	live := make([]string, 0, len(ids))
	for _, id := range ids {
		sub := s.liveSub(id)
		if sub == nil {
			gone = append(gone, id)
			continue
		}
		notifs = append(notifs, pollOneForStream(id, sub))
		live = append(live, id)
	}
	msg := getStreamingEventsResponseMessage{
		ResponseClass: "Success", ResponseCode: "NoError", Notifications: notifs,
	}
	if len(gone) > 0 {
		msg.ResponseClass = "Error"
		msg.ResponseCode = "ErrorInvalidSubscription"
		msg.ErrorSubs = &errorSubs{IDs: gone}
	}
	return msg, live
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
// write or the flush fails, the signal that the client has gone.
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

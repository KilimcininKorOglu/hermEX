package ews

import (
	"encoding/xml"
	"errors"
	"net/http"
	"time"

	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// --- SendNotification envelope (server → client callback) ---

// sendNotification is the SendNotification body POSTed to a push subscriber's
// callback (MS-OXWSNTIF). It carries one ResponseMessage whose Notification holds
// the change events (or a StatusEvent heartbeat).
type sendNotification struct {
	XMLName  xml.Name                          `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SendNotification"`
	Messages []sendNotificationResponseMessage `xml:"ResponseMessages>SendNotificationResponseMessage"`
}

type sendNotificationResponseMessage struct {
	ResponseClass string            `xml:"ResponseClass,attr"`
	ResponseCode  string            `xml:"ResponseCode"`
	Notification  *pushNotification `xml:"Notification"`
}

// pushNotification is the SendNotification Notification: the subscription id, a
// MoreEvents flag (always false, hermEX drains every poll), and the events.
type pushNotification struct {
	SubscriptionID string `xml:"http://schemas.microsoft.com/exchange/services/2006/types SubscriptionId"`
	MoreEvents     bool   `xml:"http://schemas.microsoft.com/exchange/services/2006/types MoreEvents"`
	Events         []notifEvent
}

// sendNotificationEnvelope marshals the SendNotification body wrapped in the SOAP
// envelope, ready to POST to the callback.
func sendNotificationEnvelope(subID string, events []notifEvent) ([]byte, error) {
	body := sendNotification{Messages: []sendNotificationResponseMessage{{
		ResponseClass: "Success",
		ResponseCode:  "NoError",
		Notification:  &pushNotification{SubscriptionID: subID, Events: events},
	}}}
	inner, err := xml.Marshal(body)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(soapEnvelopeOpen)+len(inner)+len(soapEnvelopeClose))
	out = append(out, soapEnvelopeOpen...)
	out = append(out, inner...)
	out = append(out, soapEnvelopeClose...)
	return out, nil
}

// --- push subscription handler + worker ---

// handlePushSubscribe registers a push subscription (MS-OXWSNTIF) and starts its
// callback worker. The callback URL is validated up front (the first SSRF gate);
// the rest mirrors the pull/streaming path (resolve folders, snapshot baseline).
func (s *Server) handlePushSubscribe(w http.ResponseWriter, req *pushSubscriptionReq, sess *session) {
	if err := validateCallbackURL(req.URL, s.pushAllowInternal); err != nil {
		// The rejection reason names the address the callback resolved to, so
		// returning it would let a subscriber use this endpoint to map internal
		// addresses, which is what the guard exists to prevent. Keep the detail in
		// the log and tell the client only that the callback was refused.
		s.soapFault(w, "ErrorInvalidSubscriptionRequest", "Subscribe: the callback address was refused", err)
		return
	}
	all := req.SubscribeToAllFolders
	targets := resolveTargets(req.FolderIDs)
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

	st, err := objectstore.OpenExisting(sess.mailbox)
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}
	snap, err := snapshotFolders(st, allFolders, folderIDs)
	_ = st.Close()
	if err != nil {
		s.soapFault(w, "ErrorInternalServerError", "an internal error occurred", err)
		return
	}

	statusMin := req.StatusFrequency
	if statusMin <= 0 {
		statusMin = 1 // the reference's minimum push heartbeat
	}
	id := s.registerPushSubscription(sess, allFolders, folderIDs, parseEventWants(req.EventTypes.Types), req.URL, statusMin, snap)

	writeResponse(w, subscribeResponse{Messages: []subscribeResponseMessage{{
		ResponseClass: "Success", ResponseCode: "NoError", SubscriptionID: id,
	}}})
}

// registerPushSubscription stores a push subscription and starts its worker. It
// uses a 30-minute idle timeout, like pull; the worker honors the same one.
func (s *Server) registerPushSubscription(sess *session, allFolders bool, folderIDs []int64, want eventWants, callbackURL string, statusMin int, snap map[int64]map[int64]uint64) string {
	const timeoutMin = 30
	s.subMu.Lock()
	s.subSeq++
	id := encodeSubscriptionID(s.subSeq, uint32(timeoutMin))
	now := time.Now()
	sub := &ewsSubscription{
		user:        sess.user,
		mailbox:     sess.mailbox,
		allFolders:  allFolders,
		folderIDs:   folderIDs,
		want:        want,
		created:     now,
		lastAccess:  now,
		timeout:     timeoutMin * time.Minute,
		snap:        snap,
		push:        true,
		callbackURL: callbackURL,
		statusFreq:  time.Duration(statusMin) * time.Minute,
		done:        make(chan struct{}),
	}
	s.subs[id] = sub
	s.subMu.Unlock()
	s.ensurePushClient()
	go s.pushWorker(id, sub)
	return id
}

// pushWorker delivers SendNotification callbacks for one push subscription. It wakes
// on a relay push (immediate) or every statusFreq (the fallback poll + heartbeat),
// stops when the subscription is dropped (done), when it has been idle longer than
// its timeout, when the client answers Unsubscribe, or after pushMaxFailures
// consecutive POST failures.
//
// The idle check runs on the ticker rather than on a one-shot timer, because a
// delivered callback restarts the timeout: a subscription whose callback keeps
// accepting notifications is in use, and cancelling it on a fixed deadline stops
// the client's push with no notice. An abandoned callback still reclaims it, through
// the failure budget.
func (s *Server) pushWorker(id string, sub *ewsSubscription) {
	var wake <-chan struct{}
	if s.waker != nil {
		ch, cancel := s.waker.Register(sub.mailbox)
		defer cancel()
		wake = ch
	}
	ticker := time.NewTicker(sub.statusFreq)
	defer ticker.Stop()

	failures := 0
	for {
		select {
		case <-sub.done:
			return // dropped (Unsubscribe handler or another stop path)
		case <-wake:
			if !s.pushStep(id, sub, false, &failures) {
				s.removeSubscription(id, sub.user)
				return
			}
		case <-ticker.C:
			if sub.expired(time.Now()) {
				s.removeSubscription(id, sub.user)
				return
			}
			if !s.pushStep(id, sub, true, &failures) {
				s.removeSubscription(id, sub.user)
				return
			}
		}
	}
}

// pushStep runs one poll-and-send under panic containment. A panic is one more
// failed delivery: it is logged with its stack and counted against the same budget
// as a callback error, so the EWS daemon serving every other user keeps running.
func (s *Server) pushStep(id string, sub *ewsSubscription, heartbeat bool, failures *int) bool {
	var keep bool
	err := lifecycle.Contain(func() error { keep = s.pushPollAndSend(id, sub, heartbeat, failures); return nil })
	if err == nil {
		return keep
	}
	*failures++
	if s.Logger != nil {
		fields := logging.Fields{"failures": *failures}
		if pe, ok := errors.AsType[*lifecycle.PanicError](err); ok {
			fields["stack"] = string(pe.Stack)
		}
		s.Logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.EWS, Name: "push.panic",
			Err: err.Error(), Fields: fields})
	}
	return *failures < pushMaxFailures
}

// pollPush reads the subscription's pending events with its lock held. The lock
// and the store are released by defer, so a panic in the poll leaves neither the
// subscription locked nor the mailbox marked in use.
func (s *Server) pollPush(sub *ewsSubscription) ([]notifEvent, bool) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	st, err := objectstore.OpenExisting(sub.mailbox)
	if err != nil {
		return nil, false
	}
	defer func() { _ = st.Close() }()
	poll := s.pushPoll
	if poll == nil {
		poll = pollSubscription
	}
	events, err := poll(st, sub)
	return events, err == nil
}

// pushPollAndSend polls the subscription and, when there are events (or a heartbeat
// is due), POSTs a SendNotification to the callback. It returns false to stop the
// worker: the client answered Unsubscribe, or the failure budget is exhausted. A
// wake with nothing new sends nothing.
func (s *Server) pushPollAndSend(id string, sub *ewsSubscription, heartbeat bool, failures *int) bool {
	events, ok := s.pollPush(sub)
	if !ok {
		return true // transient store or poll error; keep the subscription
	}
	if len(events) == 0 {
		if !heartbeat {
			return true // a wake with nothing to report: no POST
		}
		events = []notifEvent{statusEvent()}
	}

	body, err := sendNotificationEnvelope(id, events)
	if err != nil {
		return true
	}
	keep, derr := s.deliverPush(sub.callbackURL, body)
	if derr != nil {
		*failures++
		if s.Logger != nil {
			s.Logger.Warn(logging.EWS, "push.callback.error", logging.Fields{"failures": *failures, "err": derr.Error()})
		}
		return *failures < pushMaxFailures
	}
	*failures = 0
	// The callback took the notification, so the subscription is in use. A failed
	// delivery does NOT touch it, so a callback that stopped answering lets the
	// subscription go idle and be reclaimed.
	sub.touch()
	return keep
}

package ews

import (
	"encoding/xml"
	"net/http"
	"time"

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
// MoreEvents flag (always false — hermEX drains every poll), and the events.
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
		writeSOAPFault(w, "ErrorInvalidSubscriptionRequest", "Subscribe: "+err.Error())
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

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	snap, err := snapshotFolders(st, allFolders, folderIDs)
	st.Close()
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
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
// uses a 30-minute registry lifetime, like pull; the worker honors the same as its
// own deadline.
func (s *Server) registerPushSubscription(sess *session, allFolders bool, folderIDs []int64, want eventWants, callbackURL string, statusMin int, snap map[int64]map[int64]uint64) string {
	const timeoutMin = 30
	s.subMu.Lock()
	s.subSeq++
	id := encodeSubscriptionID(s.subSeq, uint32(timeoutMin))
	sub := &ewsSubscription{
		user:        sess.user,
		mailbox:     sess.mailbox,
		allFolders:  allFolders,
		folderIDs:   folderIDs,
		want:        want,
		created:     time.Now(),
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
// stops when the subscription is dropped (done), when its lifetime expires, when the
// client answers Unsubscribe, or after pushMaxFailures consecutive POST failures.
func (s *Server) pushWorker(id string, sub *ewsSubscription) {
	var wake <-chan struct{}
	if s.waker != nil {
		ch, cancel := s.waker.Register(sub.mailbox)
		defer cancel()
		wake = ch
	}
	ticker := time.NewTicker(sub.statusFreq)
	defer ticker.Stop()
	lifetime := time.NewTimer(sub.timeout)
	defer lifetime.Stop()

	failures := 0
	for {
		select {
		case <-sub.done:
			return // dropped (Unsubscribe handler or another stop path)
		case <-lifetime.C:
			s.removeSubscription(id, sub.user)
			return
		case <-wake:
			if !s.pushPollAndSend(id, sub, false, &failures) {
				s.removeSubscription(id, sub.user)
				return
			}
		case <-ticker.C:
			if !s.pushPollAndSend(id, sub, true, &failures) {
				s.removeSubscription(id, sub.user)
				return
			}
		}
	}
}

// pushPollAndSend polls the subscription and, when there are events (or a heartbeat
// is due), POSTs a SendNotification to the callback. It returns false to stop the
// worker: the client answered Unsubscribe, or the failure budget is exhausted. A
// wake with nothing new sends nothing.
func (s *Server) pushPollAndSend(id string, sub *ewsSubscription, heartbeat bool, failures *int) bool {
	sub.mu.Lock()
	st, err := objectstore.Open(sub.mailbox)
	if err != nil {
		sub.mu.Unlock()
		return true // transient store error; keep the subscription
	}
	events, perr := pollSubscription(st, sub)
	st.Close()
	sub.mu.Unlock()
	if perr != nil {
		return true
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
	return keep
}

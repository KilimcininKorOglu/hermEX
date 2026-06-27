package dav

import (
	"net/http"
	"time"

	"hermex/internal/notify"
)

// pushPollCadence bounds a push long-poll: a subscribed DAV client is held this long
// waiting for a wake before the server answers empty so the client re-subscribes. It is
// also the degradation floor when no notify bus is configured (the long-poll waits this
// out, equivalent to a poll at this interval).
const pushPollCadence = 30 * time.Second

// SetNotify wires the wake-bus consumer the push long-poll registers on. A nil consumer
// disables push, leaving the long-poll on its cadence floor.
func (s *Server) SetNotify(c *notify.Consumer) { s.notifier = c }

// handlePushPoll is the calendarserver-push subscription transport (#118): the client
// long-polls and the server holds the request, registered on the wake bus for the
// caller's mailbox, until a change in any daemon wakes it (answered 200, telling the
// client to re-sync) or the cadence elapses (answered 204, telling it to re-subscribe).
// Registration happens BEFORE the wait so a change between subscriptions is not missed.
// With no notify bus the wake never fires and only the cadence applies (the poll floor).
func (s *Server) handlePushPoll(w http.ResponseWriter, r *http.Request, mailbox string) {
	wake, cancel := s.notifier.Register(mailbox)
	defer cancel()
	timer := time.NewTimer(pushPollCadence)
	defer timer.Stop()
	select {
	case <-wake:
		w.WriteHeader(http.StatusOK)
	case <-timer.C:
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
	}
}

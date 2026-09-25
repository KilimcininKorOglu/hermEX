package meeting

import (
	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
)

// InstallDeliveryHooks wires this package's delivery-time passes into mta: the
// automatic meeting-request processing, the organizer-side REPLY tracking and the
// attendee-side CANCEL processing.
// Every daemon that delivers mail to a local mailbox must call it, because each one
// reaches mta.Deliver on its own; a daemon that does not leaves an invitation, a
// response or a cancellation sent through it unprocessed at the recipient. The
// hooks live in mta as variables because this package imports mta.
func InstallDeliveryHooks(logger *logging.Logger) {
	mta.OnMeetingRequest = requestHook(AutoProcess, logger)
	mta.OnMeetingReply = ProcessReply
	mta.OnMeetingCancel = ProcessCancellation
}

// autoProcessFunc is the meeting auto-processing pass, taken as a parameter so the
// hook's reporting can be exercised without a live store.
type autoProcessFunc func(*objectstore.Store, directory.Accounts, *relay.Spool, string, int64) (bool, error)

// requestHook builds the delivery-time meeting auto-processing hook. The organizer
// notification is kept local-only (a nil spool): an internal organizer is notified,
// while an external organizer is not, because auto-relaying machine-generated
// replies to arbitrary external addresses is a backscatter vector, gated separately
// like the out-of-office reply. A failure is swallowed on purpose (delivery already
// succeeded), so this reports it to the central sink: without that line the
// organizer is silently never told the request was accepted or declined, and
// nothing in the operator's log says why.
func requestHook(auto autoProcessFunc, logger *logging.Logger) func(*objectstore.Store, directory.Accounts, string, int64) bool {
	return func(st *objectstore.Store, accounts directory.Accounts, recipient string, msgID int64) bool {
		handled, err := auto(st, accounts, nil, recipient, msgID)
		if err != nil {
			logger.Emit(logging.Event{
				Level:     logging.LevelError,
				Subsystem: logging.MTA,
				Name:      "meeting.autoprocess.fail",
				User:      recipient,
				Err:       err.Error(),
			})
		}
		return handled
	}
}

package mta

import (
	"log"
	"sync/atomic"
	"time"

	"hermex/internal/directory"
)

// MessageSizeSettings is the stored inbound message size limit. The alias keeps
// callers from importing the directory package just to name the type in a closure.
type MessageSizeSettings = directory.MessageSizeSettings

// MessageSizeReader reads the stored limit. Every daemon passes
// (*directory.SQLDirectory).GetMessageSizeSettings; a test passes a stub.
type MessageSizeReader func() (MessageSizeSettings, bool, error)

// messageTooLarge is the error a send over the configured limit fails with. It is
// terminal: the message will not shrink on a retry, so a scheduled send is dropped
// rather than re-attempted forever.
type messageTooLarge struct{}

func (messageTooLarge) Error() string          { return "message exceeds the configured size limit" }
func (messageTooLarge) TerminalDelivery() bool { return true }

// ErrMessageTooLarge is returned by DeliverAndRelay when the operator's inbound
// size limit rejects a message. Submission callers surface it to the sender.
var ErrMessageTooLarge error = messageTooLarge{}

// maxMessageBytes is the operator's limit, 0 meaning no limit. SMTP enforces it
// during DATA, which is better (the bytes are refused before they are read), so
// this covers the paths that never touch an SMTP session.
var maxMessageBytes atomic.Int64

// SetMaxMessageSize installs the inbound size limit every send path is measured
// against; 0 removes it.
func SetMaxMessageSize(n int64) { maxMessageBytes.Store(n) }

// ApplyMessageSizeSettings reads the stored limit and applies it. A missing row or
// a read error leaves the current value in place, so a settings failure never
// starts rejecting mail unexpectedly. daemon names the caller in the log line.
func ApplyMessageSizeSettings(daemon string, read MessageSizeReader) {
	s, found, err := read()
	if err != nil {
		log.Printf("%s: message size settings read failed, leaving the limit unchanged: %v", daemon, err)
		return
	}
	if !found {
		return
	}
	SetMaxMessageSize(s.MaxInboundBytes)
}

// RunMessageSizeMaintenance re-applies the limit every minute so an admin change
// takes effect without a restart. It runs until the process exits.
func RunMessageSizeMaintenance(daemon string, read MessageSizeReader) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		ApplyMessageSizeSettings(daemon, read)
	}
}

// StartMessageSizeLimit applies the stored limit and keeps it current. Every
// binary that can submit mail calls it, so the operator's cap holds on the paths
// that never pass through an SMTP session (webmail, EWS, ActiveSync, ROP, DAV
// scheduling, send-later release).
func StartMessageSizeLimit(daemon string, read MessageSizeReader) {
	ApplyMessageSizeSettings(daemon, read)
	go RunMessageSizeMaintenance(daemon, read)
}

// overMessageSize reports whether raw exceeds the configured limit.
func overMessageSize(raw []byte) bool {
	limit := maxMessageBytes.Load()
	return limit > 0 && int64(len(raw)) > limit
}

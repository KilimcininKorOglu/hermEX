package webmail2api

import (
	"io"
	"sync/atomic"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// defaultLogger is the activity log this package reports to, installed once by the
// daemon at startup (the same pattern objectstore and mta use, which keeps
// NewServer's signature out of the wiring). A nil default, the test and library
// baseline, discards events.
var defaultLogger atomic.Pointer[logging.Logger]

// SetDefaultLogger installs the central activity log. A daemon calls it once at
// startup; passing nil disables logging from this package.
func SetDefaultLogger(l *logging.Logger) {
	defaultLogger.Store(l)
}

// logError records a failure on a path that deliberately keeps going: a Sent copy
// that could not be filed, a session that could not be revoked, a grant that
// skipped a subfolder. The request still succeeds for the user, so this line is
// the only trace the operator gets; without it a storage fault that breaks every
// Sent copy on the instance produces no signal anywhere.
// writeBody writes a response body after the status is sent. A failed write
// means the client went away mid-response; nothing can be answered any more, so
// it is recorded.
func writeBody(w io.Writer, b []byte) {
	if _, err := w.Write(b); err != nil {
		logError("response-write", err, logging.Fields{"bytes": len(b)})
	}
}

func logError(op string, err error, f logging.Fields) {
	l := defaultLogger.Load()
	if l == nil || err == nil {
		return
	}
	if f == nil {
		f = logging.Fields{}
	}
	f["op"] = op
	l.Emit(logging.Event{
		Level:     logging.LevelError,
		Subsystem: logging.Webmail,
		Name:      "error",
		Fields:    f,
		Err:       err.Error(),
	})
}

// closeStore closes a mailbox store and records a failed close. The request's
// outcome is already decided, so the failure is the operator's only trace.
func closeStore(st *objectstore.Store) {
	if err := st.Close(); err != nil {
		logError("store-close", err, logging.Fields{"store": st.Dir()})
	}
}

// fileSentCopy files a copy of an outgoing message in Sent Items. The message is
// already delivered by the time this runs, so a failure never fails the request:
// it is recorded instead, so "delivered, Sent copy filed" and "delivered, Sent
// copy lost" are distinguishable in the operator's log rather than identical.
func fileSentCopy(st *objectstore.Store, raw []byte, user, kind string) {
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, time.Now(), objectstore.FlagSeen); err != nil {
		logError("file-sent-copy", err, logging.Fields{"user": user, "kind": kind})
	}
}

// fileCallerSentCopy files the Sent copy of a message the caller sent into the
// caller's own mailbox, which is not mb.st when mb is a shared mailbox.
func (s *Server) fileCallerSentCopy(mb *mailboxCtx, c sessionClaims, raw []byte, kind string) {
	if !mb.shared {
		fileSentCopy(mb.st, raw, c.Email, kind)
		return
	}
	own, err := objectstore.OpenExisting(c.Mailbox)
	if err != nil {
		logError("file-sent-copy", err, logging.Fields{"user": c.Email, "kind": kind})
		return
	}
	defer closeStore(own)
	fileSentCopy(own, raw, c.Email, kind)
}

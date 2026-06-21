package activesync

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/serve"
)

// SessionRecorder persists live ActiveSync session telemetry for the admin
// "Mobile devices" monitor. *directory.SQLDirectory satisfies it. It is optional:
// when the server's Sessions field is nil, no telemetry is written.
type SessionRecorder interface {
	UpsertSession(directory.SessionRecord) error
}

// newSessionID mints a synthetic per-connection id. It must NOT be the OS pid: a
// single long-lived Go process has one pid, so keying telemetry on it would
// collapse every concurrent session onto one row. A random id keeps concurrent
// requests from the same user/device distinct, matching the reference's
// one-record-per-connection model.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand failure is unexpected; fall back to a time-based id so telemetry
		// degrades rather than panicking the request.
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000000")))
	}
	return hex.EncodeToString(b[:])
}

// beginSession opens telemetry for a request: it mints a per-connection id and
// writes the initial (still-running) row. A no-op when telemetry is disabled.
// Best-effort — a write failure leaves the session simply unmonitored and never
// affects the command.
func (s *Server) beginSession(r *http.Request, sess *session) {
	if s.Sessions == nil {
		return
	}
	now := time.Now().Unix()
	sess.tel = directory.SessionRecord{
		ID:         newSessionID(),
		Username:   sess.user,
		DeviceID:   sess.req.deviceID,
		DeviceType: sess.req.deviceType,
		UserAgent:  r.Header.Get("User-Agent"),
		IP:         serve.ClientAddr(r),
		Command:    sess.req.cmd,
		ASVersion:  sess.protocol,
		StartAt:    now,
		LastUpdate: now,
		Push:       sess.req.cmd == "Ping",
	}
	sess.telOn = true
	if err := s.Sessions.UpsertSession(sess.tel); err != nil {
		s.logSessionFail("session.begin.fail", sess, err)
	}
}

// logSessionFail records a best-effort telemetry write failure at debug, matching
// recordDevice's idiom so a silently-dead monitor (a column/type mismatch on the
// real write path that tests with a fake recorder never exercise) is diagnosable
// instead of invisible.
func (s *Server) logSessionFail(name string, sess *session, err error) {
	s.Logger.Emit(logging.Event{
		Level:      logging.LevelDebug,
		Subsystem:  logging.ActiveSync,
		Name:       name,
		User:       sess.user,
		RemoteAddr: sess.tel.IP,
		Fields:     logging.Fields{"device": sess.req.deviceID, "error": err.Error()},
	})
}

// touchSession refreshes a running session's last-update so a long-held request
// (a Ping heartbeat, a hanging Sync) stays visible past the staleness window
// instead of ageing out mid-flight. A no-op when telemetry is disabled.
func (s *Server) touchSession(sess *session) {
	if !sess.telOn {
		return
	}
	sess.tel.LastUpdate = time.Now().Unix()
	_ = s.Sessions.UpsertSession(sess.tel)
}

// finishSession stamps the session ended so the monitor shows it terminating and
// the sweep can remove it. Deferred at dispatch, so it runs on every exit path.
func (s *Server) finishSession(sess *session) {
	if !sess.telOn {
		return
	}
	now := time.Now().Unix()
	sess.tel.LastUpdate = now
	sess.tel.EndedAt = now
	if err := s.Sessions.UpsertSession(sess.tel); err != nil {
		s.logSessionFail("session.finish.fail", sess, err)
	}
}

package mapihttp

import (
	"context"
	"io"
	"net/http"
	"time"

	"hermex/internal/logging"
	"hermex/internal/oxmapihttp"
	"hermex/internal/rop"
)

// MAPI/HTTP notification long-poll tuning. PollsMax is the window the Connect
// response advertises as the client's NotificationWait timeout ([MS-OXCMAPIHTTP]
// 2.2.4.1.2); 60000ms (60s) matches what Exchange advertises. The server holds a wait
// a touch under that so its reply reaches the client before the client gives up,
// re-checking the shared store at notifyPollCadence (the ActiveSync Ping cadence).
const (
	pollsMaxMs              uint32 = 60000
	notifyWaitInterval             = 50 * time.Second
	notifyPollCadence              = 5 * time.Second
	flagNotificationPending uint32 = 0x00000001 // EventPending: events are queued; call Execute to drain them
)

// serveEmsmdb authenticates and dispatches the EMSMDB endpoint (/mapi/emsmdb) by
// the X-RequestType header. Every request is HTTP Basic authenticated and must
// carry X-RequestId and X-ClientInfo.
func (s *Server) serveEmsmdb(w http.ResponseWriter, r *http.Request) {
	user, mailbox, ok := s.basicAuth(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reqType := r.Header.Get("X-RequestType")
	if r.Header.Get("X-RequestId") == "" || r.Header.Get("X-ClientInfo") == "" {
		writeRespError(w, r, reqType, rcMissingHeader)
		return
	}
	sess := &session{user: user, mailbox: mailbox}
	// Log the MAPI session lifecycle (logon/logoff); the high-frequency Execute
	// batch is left to the request-level http.request log to avoid flooding.
	if reqType == "Connect" || reqType == "Disconnect" {
		s.mapiEvent(r, logging.LevelInfo, logging.ROP, "session", user, logging.Fields{"req": reqType})
	}
	switch reqType {
	case "Connect":
		s.emsConnect(w, r, sess)
	case "Execute":
		s.emsExecute(w, r, sess)
	case "Disconnect":
		s.emsDisconnect(w, r)
	case "NotificationWait":
		s.emsNotificationWait(w, r)
	case "PING":
		writeNormal(w, r, "PING", nil)
	default:
		writeRespError(w, r, reqType, rcInvalidReqType)
	}
}

// emsConnect establishes a session context and returns the sid + sequence
// cookies. The Connect request body ([MS-OXCMAPIHTTP] 2.2.4.1.1) is validated
// for shape; the session is keyed by the Basic-authenticated user, not the
// request UserDn.
func (s *Server) emsConnect(w http.ResponseWriter, r *http.Request, sess *session) {
	body, _ := io.ReadAll(r.Body)
	rd := &reader{b: body}
	rd.cstr() // UserDn
	rd.u32()  // Flags
	rd.u32()  // DefaultCodePage
	rd.u32()  // LcidString
	rd.u32()  // LcidSort
	if rd.err {
		writeRespError(w, r, "Connect", rcInvalidReqBody)
		return
	}
	sid, sequence := s.sessions.create(sess.user, sess.mailbox, s.accounts)
	setCookie(w, "sid", sid)
	setCookie(w, "sequence", sequence)

	var out writer
	out.u32(rcSuccess)  // StatusCode
	out.u32(0)          // ErrorCode
	out.u32(pollsMaxMs) // PollsMax (ms)
	out.u32(60)         // RetryCount
	out.u32(10)         // RetryDelay
	out.str("")         // DnPrefix (ASCII)
	out.wstr(sess.user) // DisplayName (UTF-16LE)
	out.u32(0)          // AuxiliaryBufferSize
	writeNormal(w, r, "Connect", out.b)
}

// emsExecute carries one ROP request buffer: it validates the session cookies,
// decodes the RPC_HEADER_EXT-framed request ROPs, dispatches them against the
// session's object/handle table, and re-frames the response ROPs.
func (s *Server) emsExecute(w http.ResponseWriter, r *http.Request, sess *session) {
	sid, errSid := r.Cookie("sid")
	seq, errSeq := r.Cookie("sequence")
	if errSid != nil || errSeq != nil {
		writeRespError(w, r, "Execute", rcMissingCookie)
		return
	}
	newSeq, ctx, code := s.sessions.execute(sid.Value, seq.Value, sess.user)
	if code != rcSuccess {
		writeRespError(w, r, "Execute", code)
		return
	}
	// The sequence is rolled atomically by execute; advertise the new value
	// before any later error return so the client stays in lockstep.
	setCookie(w, "sequence", newSeq)

	body, _ := io.ReadAll(r.Body)
	rd := &reader{b: body}
	rd.u32()                     // Flags
	cbIn := rd.u32()             // RopBufferSize
	ropBuf := rd.take(int(cbIn)) // RopBuffer
	rd.u32()                     // MaxRopOut
	if rd.err {
		writeRespError(w, r, "Execute", rcInvalidReqBody)
		return
	}

	// An empty buffer is a valid no-op; a non-empty buffer that fails to decode
	// is a malformed request. A decoded buffer is dispatched against the table.
	var respRop []byte
	switch {
	case len(ropBuf) == 0:
		respRop = oxmapihttp.EncodeExecute(nil, nil)
	default:
		reqRops, reqHandles, err := oxmapihttp.DecodeExecute(ropBuf)
		if err != nil {
			writeRespError(w, r, "Execute", rcInvalidReqBody)
			return
		}
		respRops, respHandles := ctx.ropSess.Dispatch(reqRops, reqHandles)
		respRop = oxmapihttp.EncodeExecute(respRops, respHandles)
	}

	var out writer
	out.u32(rcSuccess)            // StatusCode
	out.u32(0)                    // ErrorCode
	out.u32(0)                    // Flags
	out.u32(uint32(len(respRop))) // RopBufferSize
	out.raw(respRop)              // RopBuffer
	out.u32(0)                    // AuxiliaryBufferSize
	writeNormal(w, r, "Execute", out.b)
}

// emsDisconnect drops the session context.
func (s *Server) emsDisconnect(w http.ResponseWriter, r *http.Request) {
	sid, err := r.Cookie("sid")
	if err != nil {
		writeRespError(w, r, "Disconnect", rcMissingCookie)
		return
	}
	s.sessions.drop(sid.Value)

	var out writer
	out.u32(rcSuccess) // StatusCode
	out.u32(0)         // ErrorCode
	out.u32(0)         // AuxiliaryBufferSize
	writeNormal(w, r, "Disconnect", out.b)
}

// emsNotificationWait is the notification long-poll. hermEX has no central daemon to
// push from, so it polls the session's subscriptions against the shared store (the
// same model as the IMAP poll and ActiveSync Ping loops) and reports
// FLAG_NOTIFICATION_PENDING the moment a subscribed change appears, or a clear flag
// once the hold elapses. The reply is only a wake signal — the matching RopNotify
// bytes follow on the client's next Execute drain. Only folder- and message-scoped
// subscriptions wake it; a whole-store subscription is accepted at registration but
// not yet polled (D.Inc 2b), so a client that registers only whole-store will not be
// woken here.
func (s *Server) emsNotificationWait(w http.ResponseWriter, r *http.Request) {
	sid, err := r.Cookie("sid")
	if err != nil {
		writeRespError(w, r, "NotificationWait", rcMissingCookie)
		return
	}
	ctx := s.sessions.lookup(sid.Value)
	if ctx == nil {
		writeRespError(w, r, "NotificationWait", rcInvalidCtxCookie)
		return
	}

	var flags uint32
	if s.waitForNotification(r.Context(), ctx.ropSess) {
		flags = flagNotificationPending
	}

	var out writer
	out.u32(rcSuccess) // StatusCode
	out.u32(0)         // ErrorCode
	out.u32(flags)     // EventPending flags
	out.u32(0)         // AuxiliaryBufferSize
	writeNormal(w, r, "NotificationWait", out.b)
}

// waitForNotification holds the request open, polling the session for a deliverable
// notification until one appears (true) or the hold elapses (false). It polls before
// each sleep so an already-pending change returns on the first iteration, and bails
// immediately if the client drops the connection.
func (s *Server) waitForNotification(reqCtx context.Context, sess *rop.Session) bool {
	deadline := time.Now().Add(s.notifyWait)
	for {
		if sess.PollForChange() {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		select {
		case <-reqCtx.Done():
			return false
		case <-time.After(min(s.notifyCadence, remaining)):
		}
	}
}

// setCookie sets a MAPI/HTTP session cookie scoped to the EMSMDB endpoint.
func setCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/mapi/emsmdb"})
}

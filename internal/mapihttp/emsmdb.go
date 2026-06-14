package mapihttp

import (
	"io"
	"net/http"

	"hermex/internal/oxmapihttp"
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
	sid, sequence := s.sessions.create(sess.user, sess.mailbox)
	setCookie(w, "sid", sid)
	setCookie(w, "sequence", sequence)

	var out writer
	out.u32(rcSuccess)  // StatusCode
	out.u32(0)          // ErrorCode
	out.u32(60000)      // PollsMax (ms)
	out.u32(60)         // RetryCount
	out.u32(10)         // RetryDelay
	out.str("")         // DnPrefix (ASCII)
	out.wstr(sess.user) // DisplayName (UTF-16LE)
	out.u32(0)          // AuxiliaryBufferSize
	writeNormal(w, r, "Connect", out.b)
}

// emsExecute carries one ROP request buffer. The skeleton validates the session
// cookies and frames an empty ROP response; the ROP buffer codec and dispatch
// land in the next increment.
func (s *Server) emsExecute(w http.ResponseWriter, r *http.Request, sess *session) {
	sid, errSid := r.Cookie("sid")
	seq, errSeq := r.Cookie("sequence")
	if errSid != nil || errSeq != nil {
		writeRespError(w, r, "Execute", rcMissingCookie)
		return
	}
	newSeq, _, code := s.sessions.execute(sid.Value, seq.Value, sess.user)
	if code != rcSuccess {
		writeRespError(w, r, "Execute", code)
		return
	}

	body, _ := io.ReadAll(r.Body)
	rd := &reader{b: body}
	rd.u32()           // Flags
	cbIn := rd.u32()   // RopBufferSize
	rd.take(int(cbIn)) // RopBuffer (parsed and dispatched by the ROP layer in 4-C)
	rd.u32()           // MaxRopOut
	if rd.err {
		writeRespError(w, r, "Execute", rcInvalidReqBody)
		return
	}
	setCookie(w, "sequence", newSeq)

	// The skeleton frames an empty ROP response buffer — a valid, final
	// RPC_HEADER_EXT envelope. The ROP layer fills in the per-ROP responses.
	respRop := oxmapihttp.EncodeExecute(nil, nil)
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

// emsNotificationWait answers the notification long-poll. v1 has no live push,
// so it returns immediately with no events pending.
func (s *Server) emsNotificationWait(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie("sid"); err != nil {
		writeRespError(w, r, "NotificationWait", rcMissingCookie)
		return
	}
	var out writer
	out.u32(rcSuccess) // StatusCode
	out.u32(0)         // ErrorCode
	out.u32(0)         // EventPending flags (none)
	writeNormal(w, r, "NotificationWait", out.b)
}

// setCookie sets a MAPI/HTTP session cookie scoped to the EMSMDB endpoint.
func setCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/mapi/emsmdb"})
}

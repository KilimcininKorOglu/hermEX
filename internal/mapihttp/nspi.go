package mapihttp

import (
	"io"
	"net/http"
)

// serveNspi authenticates and dispatches the NSPI endpoint (/mapi/nspi) by the
// X-RequestType header. Bind establishes the session (sid + sequence cookies);
// the remaining address-book ops run within it. PING is a session-less liveness
// probe. The GAL browse/resolve ops (QueryRows/ResolveNamesW/...) land in later
// sub-slices and currently report an invalid request type.
func (s *Server) serveNspi(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.basicAuth(w, r)
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
	switch reqType {
	case "PING":
		writeNormal(w, r, "PING", nil)
	case "Bind":
		s.nspiBind(w, r, user)
	case "Unbind":
		s.nspiUnbind(w, r)
	case "GetSpecialTable":
		s.nspiOp(w, r, user, "GetSpecialTable", s.nsp.GetSpecialTable)
	case "QueryRows":
		s.nspiOp(w, r, user, "QueryRows", s.nsp.QueryRows)
	case "UpdateStat":
		s.nspiOp(w, r, user, "UpdateStat", s.nsp.UpdateStat)
	case "QueryColumns":
		s.nspiOp(w, r, user, "QueryColumns", s.nsp.QueryColumns)
	default:
		writeRespError(w, r, reqType, rcInvalidReqType)
	}
}

// nspiOp runs a sequenced NSPI op (everything past Bind/Unbind/PING): it
// validates the session cookies, rolls the sequence, decodes the request body,
// runs handler, and frames the response. handler maps the request body to the
// NSPI response body.
func (s *Server) nspiOp(w http.ResponseWriter, r *http.Request, user, reqType string, handler func([]byte) []byte) {
	sid, errSid := r.Cookie("sid")
	seq, errSeq := r.Cookie("sequence")
	if errSid != nil || errSeq != nil {
		writeRespError(w, r, reqType, rcMissingCookie)
		return
	}
	newSeq, code := s.nspiSessions.validate(sid.Value, seq.Value, user)
	if code != rcSuccess {
		writeRespError(w, r, reqType, code)
		return
	}
	setNspiCookie(w, "sequence", newSeq)
	body, _ := io.ReadAll(r.Body)
	writeNormal(w, r, reqType, handler(body))
}

// nspiBind decodes the Bind request, runs it against the NSPI server, and — only
// when the bind succeeds — establishes the sid + sequence session cookies.
func (s *Server) nspiBind(w http.ResponseWriter, r *http.Request, user string) {
	body, _ := io.ReadAll(r.Body)
	resp, ok := s.nsp.Bind(body)
	if ok {
		sid, sequence := s.nspiSessions.bind(user)
		setNspiCookie(w, "sid", sid)
		setNspiCookie(w, "sequence", sequence)
	}
	writeNormal(w, r, "Bind", resp)
}

// nspiUnbind drops the bound session (keyed by the sid cookie) and returns the
// Unbind response. A request without a session cookie is rejected, matching the
// transport's missing-cookie contract.
func (s *Server) nspiUnbind(w http.ResponseWriter, r *http.Request) {
	sid, err := r.Cookie("sid")
	if err != nil {
		writeRespError(w, r, "Unbind", rcMissingCookie)
		return
	}
	body, _ := io.ReadAll(r.Body)
	s.nspiSessions.drop(sid.Value)
	writeNormal(w, r, "Unbind", s.nsp.Unbind(body))
}

// setNspiCookie sets a MAPI/HTTP session cookie scoped to the NSPI endpoint, so
// it never collides with the EMSMDB endpoint's cookies of the same name.
func setNspiCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/mapi/nspi"})
}

package mapihttp

import (
	"io"
	"net/http"

	"hermex/internal/logging"
)

// serveNspi authenticates and dispatches the NSPI endpoint (/mapi/nspi) by the
// X-RequestType header. Bind establishes the session (sid + sequence cookies);
// the remaining address-book ops run within it. PING is a session-less liveness
// probe. The full online address-book op set Outlook uses to browse, navigate,
// and resolve against the GAL is served, plus ModLinkAtt (editing the caller's
// own delegate list); the other write/template ops (ModProps, GetTemplateInfo)
// report an invalid request type.
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
	case "ResolveNames":
		s.nspiOp(w, r, user, "ResolveNames", s.nsp.ResolveNamesW)
	case "DNToMId":
		s.nspiOp(w, r, user, "DNToMId", s.nsp.DNToMId)
	case "GetMatches":
		s.nspiOp(w, r, user, "GetMatches", s.nsp.GetMatches)
	case "GetProps":
		s.nspiOp(w, r, user, "GetProps", s.nsp.GetProps)
	case "GetPropList":
		s.nspiOp(w, r, user, "GetPropList", s.nsp.GetPropList)
	case "SeekEntries":
		s.nspiOp(w, r, user, "SeekEntries", s.nsp.SeekEntries)
	case "CompareMIds":
		s.nspiOp(w, r, user, "CompareMIds", s.nsp.CompareMids)
	case "ResortRestriction":
		s.nspiOp(w, r, user, "ResortRestriction", s.nsp.ResortRestriction)
	case "ModLinkAtt":
		s.nspiOpAuth(w, r, user, "ModLinkAtt", s.nsp.ModLinkAtt)
	default:
		writeRespError(w, r, reqType, rcInvalidReqType)
	}
}

// nspiOp runs a sequenced NSPI op whose handler needs only the request body. It
// is nspiOpAuth with the authenticated user discarded.
func (s *Server) nspiOp(w http.ResponseWriter, r *http.Request, user, reqType string, handler func([]byte) []byte) {
	s.nspiOpAuth(w, r, user, reqType, func(body []byte, _ string) []byte {
		return handler(body)
	})
}

// nspiOpAuth runs a sequenced NSPI op (everything past Bind/Unbind/PING): it
// validates the session cookies, rolls the sequence, decodes the request body,
// runs handler, and frames the response. handler also receives the authenticated
// user, which an identity-bearing op (ModLinkAtt) needs for its access check.
func (s *Server) nspiOpAuth(w http.ResponseWriter, r *http.Request, user, reqType string, handler func([]byte, string) []byte) {
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
	s.mapiEvent(r, logging.LevelDebug, logging.NSPI, "operation", user, logging.Fields{"op": reqType})
	body, _ := io.ReadAll(r.Body)
	writeNormal(w, r, reqType, handler(body, user))
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
	s.mapiEvent(r, logging.LevelInfo, logging.NSPI, "bind", user, logging.Fields{"ok": ok})
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

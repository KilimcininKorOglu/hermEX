package mapihttp

import (
	"net/http"

	"hermex/internal/logging"
	"hermex/internal/nspi"
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
	default:
		s.nspiDispatch(w, r, user, reqType)
	}
}

// nspiPublicOps are the address-book ops that serve the same answer to everyone:
// the static column set and the special table.
var nspiPublicOps = map[string]func(*nspi.Server, []byte) []byte{
	"GetSpecialTable": (*nspi.Server).GetSpecialTable,
	"QueryColumns":    (*nspi.Server).QueryColumns,
}

// nspiAuthedOps are the address-book ops scoped to the caller: the entries they
// may return depend on who is asking, and the delegate write op needs the
// identity for its owner-only access check.
var nspiAuthedOps = map[string]func(*nspi.Server, []byte, string) []byte{
	"QueryRows":         (*nspi.Server).QueryRows,
	"UpdateStat":        (*nspi.Server).UpdateStat,
	"ResolveNames":      (*nspi.Server).ResolveNamesW,
	"DNToMId":           (*nspi.Server).DNToMId,
	"GetMatches":        (*nspi.Server).GetMatches,
	"GetProps":          (*nspi.Server).GetProps,
	"GetPropList":       (*nspi.Server).GetPropList,
	"SeekEntries":       (*nspi.Server).SeekEntries,
	"CompareMIds":       (*nspi.Server).CompareMids,
	"ResortRestriction": (*nspi.Server).ResortRestriction,
	"ModLinkAtt":        (*nspi.Server).ModLinkAtt,
}

// nspiDispatch runs a sequenced address-book op. A request type in neither table
// is refused as an invalid request type.
func (s *Server) nspiDispatch(w http.ResponseWriter, r *http.Request, user, reqType string) {
	if op, ok := nspiPublicOps[reqType]; ok {
		s.nspiOp(w, r, user, reqType, func(body []byte) []byte { return op(s.nsp, body) })
		return
	}
	op, ok := nspiAuthedOps[reqType]
	if !ok {
		writeRespError(w, r, reqType, rcInvalidReqType)
		return
	}
	s.nspiOpAuth(w, r, user, reqType, func(body []byte, caller string) []byte {
		return op(s.nsp, body, caller)
	})
}

// nspiOp runs a sequenced NSPI op whose handler needs only the request body. It
// is nspiOpAuth with the authenticated user discarded, for the two ops that
// serve the same answer to everyone (the static column set and the special
// table); every op that reads the address book itself uses nspiOpAuth, since the
// entries it may return depend on who is asking.
func (s *Server) nspiOp(w http.ResponseWriter, r *http.Request, user, reqType string, handler func([]byte) []byte) {
	s.nspiOpAuth(w, r, user, reqType, func(body []byte, _ string) []byte {
		return handler(body)
	})
}

// nspiOpAuth runs a sequenced NSPI op (everything past Bind/Unbind/PING): it
// validates the session cookies, rolls the sequence, decodes the request body,
// runs handler, and frames the response. handler also receives the authenticated
// user: the address book is scoped to the caller, and the delegate write op needs
// the identity for its owner-only access check.
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
	body := readBody(r)
	writeNormal(w, r, reqType, handler(body, user))
}

// nspiBind decodes the Bind request, runs it against the NSPI server, and, only
// when the bind succeeds, establishes the sid + sequence session cookies.
func (s *Server) nspiBind(w http.ResponseWriter, r *http.Request, user string) {
	body := readBody(r)
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
	body := readBody(r)
	s.nspiSessions.drop(sid.Value)
	writeNormal(w, r, "Unbind", s.nsp.Unbind(body))
}

// setNspiCookie sets a MAPI/HTTP session cookie scoped to the NSPI endpoint, so
// it never collides with the EMSMDB endpoint's cookies of the same name.
// HttpOnly prevents JavaScript access; Secure restricts transmission to HTTPS.
// SameSite is inert for the MAPI clients this endpoint serves, and it closes the
// cross-site path for the one client that does honor it, a browser.
func setNspiCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/mapi/nspi",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

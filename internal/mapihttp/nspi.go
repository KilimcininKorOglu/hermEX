package mapihttp

import "net/http"

// serveNspi authenticates and frames the NSPI endpoint (/mapi/nspi). The
// address-book calls (Bind/GetMatches/QueryRows/ResolveNamesW/...) land in a
// later sub-slice; the skeleton answers PING and rejects other request types
// with an invalid-request-type response code, so the transport is exercisable
// before the address-book layer exists.
func (s *Server) serveNspi(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.basicAuth(w, r); !ok {
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
	if reqType == "PING" {
		writeNormal(w, r, "PING", nil)
		return
	}
	writeRespError(w, r, reqType, rcInvalidReqType)
}

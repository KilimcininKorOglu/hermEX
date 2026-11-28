package ews

import (
	"encoding/json"
	"net/http"
	"strings"

	"hermex/internal/logging"
	"hermex/internal/serve"
)

// autodiscoverJSONPrefix is the Autodiscover v2 path: a client asks for one
// protocol's endpoint with GET /autodiscover/autodiscover.json/v1.0/<address>
// ?Protocol=<name> (or ?Email=&Protocol= on the bare path) and gets back the
// protocol name and its URL as JSON. Like Exchange, the request is not
// authenticated; the answer names only the host-global endpoint, so it says
// nothing about whether the address exists.
const autodiscoverJSONPrefix = "/autodiscover/autodiscover.json"

type adJSONReply struct {
	Protocol string `json:"Protocol"`
	URL      string `json:"Url"`
}

type adJSONError struct {
	ErrorCode    string `json:"ErrorCode"`
	ErrorMessage string `json:"ErrorMessage"`
}

// adJSONPaths maps a lower-case protocol name to the path of its endpoint.
var adJSONPaths = map[string]struct{ name, path string }{
	"ews":            {"EWS", "/EWS/Exchange.asmx"},
	"activesync":     {"ActiveSync", "/Microsoft-Server-ActiveSync"},
	"autodiscoverv1": {"AutodiscoverV1", "/autodiscover/autodiscover.xml"},
}

// serveAutodiscoverJSON answers an Autodiscover v2 request.
func (s *Server) serveAutodiscoverJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	proto, ok := adJSONPaths[strings.ToLower(r.URL.Query().Get("Protocol"))]
	if !ok {
		s.writeADJSON(w, http.StatusBadRequest, adJSONError{"InvalidProtocol", "The given protocol value is invalid or not supported."})
		return
	}
	host := s.hostname
	if host == "" {
		if !serve.ValidPublicHost(r.Host) {
			http.Error(w, "autodiscover unavailable: server hostname not configured", http.StatusServiceUnavailable)
			return
		}
		host = r.Host
	}
	s.writeADJSON(w, http.StatusOK, adJSONReply{Protocol: proto.name, URL: "https://" + host + proto.path})
}

func (s *Server) writeADJSON(w http.ResponseWriter, status int, v any) {
	out, err := json.Marshal(v)
	if err != nil {
		s.Logger.Emit(logging.Event{
			Level: logging.LevelError, Subsystem: logging.EWS, Name: "autodiscover_json.marshal.fail", Err: err.Error(),
		})
		http.Error(w, "an internal error occurred", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeReply(w, append(out, '\n'), s.Logger)
}

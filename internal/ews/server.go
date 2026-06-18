// Package ews serves a mailbox over Exchange Web Services (EWS, MS-OXWS*): the
// SOAP endpoint on /EWS/Exchange.asmx and the Outlook-schema Autodiscover
// endpoint. It is a thin protocol adapter — the MAPI<->EWS-XML conversion lives
// in internal/oxews; this package handles SOAP routing, operation dispatch, and
// faults, authenticating each request with HTTP Basic against the directory and
// operating on the MAPI object store, reusing the same infrastructure as the
// IMAP, DAV, and ActiveSync daemons. v1 targets the mail (IPM.Note) item class.
package ews

import (
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// Server answers EWS and Autodiscover requests for authenticated users.
type Server struct {
	auth     directory.Authenticator
	accounts directory.Accounts
	hostname string
	Logger   *logging.Logger // central activity log; nil disables logging
}

// NewServer builds an EWS server backed by the directory for authentication and
// recipient resolution (the latter used by CreateItem/ResolveNames).
func NewServer(auth directory.Authenticator, accounts directory.Accounts, hostname string) *Server {
	return &Server{auth: auth, accounts: accounts, hostname: hostname}
}

// Handler returns the HTTP handler. One handler routes the two EWS paths by a
// case-insensitive match, since clients vary the casing of both.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	return mux
}

// route dispatches the EWS SOAP endpoint and the Outlook Autodiscover endpoint.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch strings.ToLower(r.URL.Path) {
	case "/ews/exchange.asmx":
		s.serveEWS(w, r)
	case "/autodiscover/autodiscover.xml":
		s.serveAutodiscover(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveEWS authenticates, answers OPTIONS, and dispatches a POST SOAP request.
// Every method on this endpoint is authenticated, matching Exchange behaviour.
func (s *Server) serveEWS(w http.ResponseWriter, r *http.Request) {
	user, mailbox, ok := s.basicAuth(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "OPTIONS, POST")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "OPTIONS, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.dispatch(w, r, &session{user: user, mailbox: mailbox})
}

// session carries the per-request context handed to an operation handler.
type session struct {
	user    string
	mailbox string
}

// basicAuth validates HTTP Basic credentials against the directory and returns
// the user and mailbox path. On failure it writes a 401 challenge.
func (s *Server) basicAuth(w http.ResponseWriter, r *http.Request) (user, mailbox string, ok bool) {
	u, p, hasAuth := r.BasicAuth()
	if hasAuth {
		if path, good := s.auth.Authenticate(u, p); good {
			return u, path, true
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="hermEX"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return "", "", false
}

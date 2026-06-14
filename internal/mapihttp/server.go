// Package mapihttp serves a mailbox over the native Outlook transport,
// MAPI/HTTP ([MS-OXCMAPIHTTP]): the EMSMDB endpoint on /mapi/emsmdb (the
// store/ROP channel) and the NSPI endpoint on /mapi/nspi (the address book).
// It authenticates each request with HTTP Basic against the directory — modern
// Outlook over MAPI/HTTP accepts Basic, so no NTLM subsystem is required — and
// frames responses in the application/mapi-http chunked PROCESSING/DONE form.
//
// This package owns the transport: routing, request-type dispatch, the session
// cookies, and the response framing. The ROP buffer carried inside Execute and
// the NSPI calls are decoded by their own layers; v1 targets online-mode mail.
package mapihttp

import (
	"net/http"
	"strings"

	"hermex/internal/directory"
)

// Server answers MAPI/HTTP EMSMDB and NSPI requests for authenticated users.
type Server struct {
	auth     directory.Authenticator
	accounts directory.Accounts
	hostname string
	sessions *sessionStore
}

// NewServer builds a MAPI/HTTP server backed by the directory for authentication
// (accounts is reserved for the NSPI GAL and ROP recipient resolution).
func NewServer(auth directory.Authenticator, accounts directory.Accounts, hostname string) *Server {
	return &Server{auth: auth, accounts: accounts, hostname: hostname, sessions: newSessionStore()}
}

// Handler returns the HTTP handler. One handler routes the two MAPI/HTTP paths;
// each carries a ?MailboxId= query, so the match is on the path prefix.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	return mux
}

// route dispatches the EMSMDB and NSPI endpoints by path prefix (case folded).
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch p := strings.ToLower(r.URL.Path); {
	case strings.HasPrefix(p, "/mapi/emsmdb"):
		s.serveEmsmdb(w, r)
	case strings.HasPrefix(p, "/mapi/nspi"):
		s.serveNspi(w, r)
	default:
		http.NotFound(w, r)
	}
}

// session carries the per-request authenticated identity handed to a handler.
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

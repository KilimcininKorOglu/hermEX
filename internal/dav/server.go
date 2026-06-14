// Package dav serves a mailbox's contacts (and, later, calendars) over CardDAV
// and CalDAV (RFC 6352 / RFC 4791) on top of the MAPI object store. It is a thin
// protocol adapter: vCard/iCalendar conversion lives in the store-layer
// converters (internal/oxvcard, internal/oxcical); this package handles WebDAV
// routing, PROPFIND/REPORT, ETags, and sync tokens, authenticating each request
// with HTTP Basic against the directory.
package dav

import (
	"net/http"
	"strings"

	"hermex/internal/directory"
)

// Server answers DAV requests for the authenticated user's mailbox.
type Server struct {
	auth     directory.Authenticator
	accounts directory.Accounts
	hostname string
}

// NewServer builds a DAV server backed by the directory for authentication and
// account resolution.
func NewServer(auth directory.Authenticator, accounts directory.Accounts, hostname string) *Server {
	return &Server{auth: auth, accounts: accounts, hostname: hostname}
}

// Handler returns the HTTP handler for the DAV server. Every path is routed
// through one handler that authenticates first, then dispatches by method —
// a single DAV URL serves many methods (PROPFIND/REPORT/GET/PUT/DELETE), so
// method-pattern muxing is the wrong model.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	return mux
}

// route authenticates, then dispatches on the HTTP method.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	user, mailbox, ok := s.basicAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case "OPTIONS":
		s.handleOptions(w, r)
	case "PROPFIND":
		s.handlePropfind(w, r, user, mailbox)
	default:
		w.Header().Set("Allow", allowMethods)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// allowMethods lists the methods implemented so far; later increments add the
// object lifecycle (GET/PUT/DELETE) and REPORT.
const allowMethods = "OPTIONS, PROPFIND"

// basicAuth validates HTTP Basic credentials against the directory and returns
// the user and mailbox path. On failure it writes a 401 challenge and returns
// ok=false.
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

// handleOptions advertises DAV capabilities. addressbook signals CardDAV support
// (RFC 6352 §6.1); levels 1 and 3 cover core WebDAV and PROPFIND/REPORT.
func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("DAV", "1, 3, addressbook")
	w.Header().Set("Allow", allowMethods)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// resourceKind classifies a DAV URL path.
type resourceKind int

const (
	kindUnknown     resourceKind = iota
	kindRoot                     // /dav/ or /
	kindPrincipal                // /dav/principals/{user}/
	kindHomeSet                  // /dav/addressbooks/{user}/
	kindAddressbook              // /dav/addressbooks/{user}/contacts/
	kindObject                   // /dav/addressbooks/{user}/contacts/{name}
)

// addressbookName is the single address book each mailbox exposes (its Contacts
// folder). The URL space is /dav/addressbooks/{user}/contacts/.
const addressbookName = "contacts"

// classify parses a request path into a resource kind plus, for an object, its
// resource name. It does not consult the store.
func classify(p string) (kind resourceKind, user, object string) {
	switch p {
	case "", "/", "/dav", "/dav/":
		return kindRoot, "", ""
	}
	trimmed := strings.Trim(p, "/")
	parts := strings.Split(trimmed, "/")
	// parts[0] == "dav"
	if len(parts) < 2 || parts[0] != "dav" {
		return kindUnknown, "", ""
	}
	switch parts[1] {
	case "principals":
		if len(parts) >= 3 {
			return kindPrincipal, parts[2], ""
		}
	case "addressbooks":
		switch len(parts) {
		case 3:
			return kindHomeSet, parts[2], ""
		case 4:
			if parts[3] == addressbookName {
				return kindAddressbook, parts[2], ""
			}
		case 5:
			if parts[3] == addressbookName {
				return kindObject, parts[2], parts[4]
			}
		}
	}
	return kindUnknown, "", ""
}

// URL builders for the fixed DAV layout.
func principalPath(user string) string { return "/dav/principals/" + user + "/" }
func homeSetPath(user string) string   { return "/dav/addressbooks/" + user + "/" }
func addressbookPath(user string) string {
	return "/dav/addressbooks/" + user + "/" + addressbookName + "/"
}
func objectPath(user, name string) string { return addressbookPath(user) + name }

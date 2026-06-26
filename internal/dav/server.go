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
	"sync/atomic"

	"hermex/internal/directory"
)

// Server answers DAV requests for the authenticated user's mailbox.
type Server struct {
	auth     directory.Authenticator
	accounts directory.Accounts
	hostname string
	// maxICal and maxVCard cap the iCalendar and vCard PUT bodies in bytes (0 = the
	// built-in defaults), held atomically so the DAV daemon's poll can apply an
	// operator's edit while requests run, with no restart. Set them via SetMaxICal /
	// SetMaxVCard; the PUT handlers read them live.
	maxICal  atomic.Int64
	maxVCard atomic.Int64
}

// NewServer builds a DAV server backed by the directory for authentication and
// account resolution.
func NewServer(auth directory.Authenticator, accounts directory.Accounts, hostname string) *Server {
	return &Server{auth: auth, accounts: accounts, hostname: hostname}
}

// SetMaxICal and SetMaxVCard set the iCalendar / vCard PUT body caps in bytes (0
// restores the built-in default). They are safe to call concurrently with request
// handling, so an operator's edit applies without a restart.
func (s *Server) SetMaxICal(n int64) {
	if n < 0 {
		n = 0
	}
	s.maxICal.Store(n)
}

// SetMaxVCard sets the vCard PUT body cap in bytes (0 restores the built-in default).
func (s *Server) SetMaxVCard(n int64) {
	if n < 0 {
		n = 0
	}
	s.maxVCard.Store(n)
}

// icalLimit and vcardLimit resolve the live PUT body caps: the operator-set value, or
// the built-in default when none is set.
func (s *Server) icalLimit() int64 {
	if v := s.maxICal.Load(); v > 0 {
		return v
	}
	return defaultMaxICal
}

func (s *Server) vcardLimit() int64 {
	if v := s.maxVCard.Load(); v > 0 {
		return v
	}
	return defaultMaxVCard
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

// route handles well-known autodiscovery, then authenticates and dispatches on
// the HTTP method.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	// RFC 6764 autodiscovery: a client bootstraps from /.well-known/{carddav,caldav}
	// and follows the redirect to the DAV root, where a PROPFIND for
	// current-user-principal continues the chain. The redirect itself is
	// unauthenticated so a client can find the root before sending credentials.
	if r.URL.Path == "/.well-known/carddav" || r.URL.Path == "/.well-known/caldav" {
		http.Redirect(w, r, "/dav/", http.StatusMovedPermanently)
		return
	}
	user, mailbox, ok := s.basicAuth(w, r)
	if !ok {
		return
	}
	// Object methods dispatch by collection: a calendar .ics object is served by
	// the CalDAV handlers, everything else by the CardDAV handlers.
	kind, _, _, _ := classify(r.URL.Path)
	calObject := kind == kindCalObject
	switch r.Method {
	case "OPTIONS":
		s.handleOptions(w, r)
	case "PROPFIND":
		s.handlePropfind(w, r, user, mailbox)
	case "REPORT":
		s.handleReport(w, r, mailbox)
	case http.MethodGet, http.MethodHead:
		if calObject {
			s.handleCalGet(w, r, mailbox)
		} else {
			s.handleGet(w, r, mailbox)
		}
	case http.MethodPut:
		if calObject {
			s.handleCalPut(w, r, mailbox)
		} else {
			s.handlePut(w, r, mailbox)
		}
	case http.MethodDelete:
		if calObject {
			s.handleCalDelete(w, r, mailbox)
		} else {
			s.handleDelete(w, r, mailbox)
		}
	case "MKCALENDAR":
		s.handleMkCalendar(w, r, mailbox)
	case "MKCOL":
		s.handleMkCol(w, r, mailbox)
	case "PROPPATCH":
		s.handleProppatch(w, r, mailbox)
	case "COPY":
		s.handleCopyMove(w, r, mailbox, false)
	case "MOVE":
		s.handleCopyMove(w, r, mailbox, true)
	default:
		w.Header().Set("Allow", allowMethods)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// allowMethods lists the DAV methods the server implements.
const allowMethods = "OPTIONS, PROPFIND, PROPPATCH, REPORT, GET, HEAD, PUT, DELETE, MKCALENDAR, MKCOL, COPY, MOVE"

// basicAuth validates HTTP Basic credentials against the directory and returns
// the user and mailbox path. On failure it writes a 401 challenge and returns
// ok=false.
func (s *Server) basicAuth(w http.ResponseWriter, r *http.Request) (user, mailbox string, ok bool) {
	u, p, hasAuth := r.BasicAuth()
	if hasAuth {
		if path, good := s.auth.Authenticate(u, p); good {
			if privs, _ := s.auth.Privileges(u); !privs.DAV {
				http.Error(w, "DAV access is disabled for this account", http.StatusForbidden)
				return "", "", false
			}
			return u, path, true
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="hermEX"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return "", "", false
}

// handleOptions advertises DAV capabilities. addressbook signals CardDAV (RFC
// 6352 §6.1) and calendar-access signals CalDAV (RFC 4791 §5.1); levels 1 and 3
// cover core WebDAV and PROPFIND/REPORT.
func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("DAV", "1, 3, addressbook, calendar-access, extended-mkcol")
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
	kindCalHomeSet               // /dav/calendars/{user}/
	kindCalendar                 // /dav/calendars/{user}/calendar/
	kindCalObject                // /dav/calendars/{user}/calendar/{name}
)

// addressbookName is the single address book each mailbox exposes (its Contacts
// folder); calendarName is the single calendar (its Calendar folder). The URL
// spaces are /dav/addressbooks/{user}/contacts/ and /dav/calendars/{user}/calendar/.
const (
	addressbookName = "contacts"
	calendarName    = "calendar"
)

// classify parses a request path into a resource kind plus, for a collection, its
// name segment and, for an object, its resource name. Any collection name is
// accepted (the well-known "calendar"/"contacts" plus user-created subfolders); the
// handler resolves the name to a folder. It does not consult the store.
func classify(p string) (kind resourceKind, user, collection, object string) {
	switch p {
	case "", "/", "/dav", "/dav/":
		return kindRoot, "", "", ""
	}
	trimmed := strings.Trim(p, "/")
	parts := strings.Split(trimmed, "/")
	// parts[0] == "dav"
	if len(parts) < 2 || parts[0] != "dav" {
		return kindUnknown, "", "", ""
	}
	switch parts[1] {
	case "principals":
		if len(parts) >= 3 {
			return kindPrincipal, parts[2], "", ""
		}
	case "addressbooks":
		switch len(parts) {
		case 3:
			return kindHomeSet, parts[2], "", ""
		case 4:
			return kindAddressbook, parts[2], parts[3], ""
		case 5:
			return kindObject, parts[2], parts[3], parts[4]
		}
	case "calendars":
		switch len(parts) {
		case 3:
			return kindCalHomeSet, parts[2], "", ""
		case 4:
			return kindCalendar, parts[2], parts[3], ""
		case 5:
			return kindCalObject, parts[2], parts[3], parts[4]
		}
	}
	return kindUnknown, "", "", ""
}

// URL builders for the fixed DAV layout.
func principalPath(user string) string { return "/dav/principals/" + user + "/" }
func homeSetPath(user string) string   { return "/dav/addressbooks/" + user + "/" }
func addressbookPath(user string) string {
	return "/dav/addressbooks/" + user + "/" + addressbookName + "/"
}
func objectPath(user, name string) string { return addressbookPath(user) + name }

// addressbookPathColl builds the href of a named address-book collection (the
// reserved "contacts" or a user-created one); an empty name means the reserved one.
func addressbookPathColl(user, coll string) string {
	if coll == "" {
		coll = addressbookName
	}
	return "/dav/addressbooks/" + user + "/" + coll + "/"
}

// objectPathColl builds an object href inside a named address-book collection, so
// REPORT/PROPFIND responses echo the same collection segment the client requested.
func objectPathColl(user, coll, name string) string {
	return addressbookPathColl(user, coll) + name
}

func calHomeSetPath(user string) string { return "/dav/calendars/" + user + "/" }
func calendarPath(user string) string {
	return "/dav/calendars/" + user + "/" + calendarName + "/"
}
func calObjectPath(user, name string) string { return calendarPath(user) + name }

// calendarPathColl builds the href of a named calendar collection; an empty name
// means the reserved "calendar".
func calendarPathColl(user, coll string) string {
	if coll == "" {
		coll = calendarName
	}
	return "/dav/calendars/" + user + "/" + coll + "/"
}

// calObjectPathColl builds an object href inside a named calendar collection.
func calObjectPathColl(user, coll, name string) string {
	return calendarPathColl(user, coll) + name
}

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
	"sync"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/notify"
	"hermex/internal/publicfolder"
	"hermex/internal/relay"
)

// Server answers EWS and Autodiscover requests for authenticated users.
type Server struct {
	auth     directory.Authenticator
	accounts directory.Accounts
	hostname string
	Logger   *logging.Logger       // central activity log; nil disables logging
	Spool    *relay.Spool          // outbound relay queue; nil sends local-only
	Pub      *publicfolder.Service // per-domain public folders; nil disables them

	// Notification subscriptions (MS-OXWSNTIF). The registry is in-memory and
	// process-local: Subscribe and GetEvents are separate HTTP requests that must
	// reach the same process, which the gateway guarantees by routing /ews to a
	// single backend target (a horizontally-scaled EWS service would break this,
	// the same per-process constraint the reference's subscription cache has).
	subMu  sync.Mutex
	subs   map[string]*ewsSubscription
	subSeq uint32 // monotonic SubscriptionId key counter (0 reserved)

	// Streaming-notification cadence and lifetime. Both zero in production: the
	// interval falls back to the default continuation cadence and the lifetime to
	// the request's ConnectionTimeout. Tests set them small to drive the loop fast.
	streamInterval time.Duration
	streamWindow   time.Duration

	waker notify.Registrar // push wake source; nil keeps streaming on its interval only

	// Push-subscription (MS-OXWSNTIF) outbound callback delivery. pushHTTP is the
	// SSRF-guarded client built once on first use; pushAllowInternal disables the
	// IP-range block for an internal/dev deployment whose callbacks are not public.
	pushOnce          sync.Once
	pushHTTP          *http.Client
	pushAllowInternal bool
}

// ensurePushClient builds the SSRF-guarded push callback client once, reading
// pushAllowInternal at that point.
func (s *Server) ensurePushClient() {
	s.pushOnce.Do(func() { s.pushHTTP = pushClient(s.pushAllowInternal) })
}

// SetNotify wires the push wake source so a held GetStreamingEvents emits a
// continuation the instant a watched mailbox changes rather than on its interval. A
// nil consumer (push disabled) leaves streaming on its interval — the degradation
// floor. The daemon calls this once at startup, before serving.
func (s *Server) SetNotify(c *notify.Consumer) {
	if c == nil {
		return
	}
	s.waker = c
}

// NewServer builds an EWS server backed by the directory for authentication and
// recipient resolution (the latter used by CreateItem/ResolveNames).
func NewServer(auth directory.Authenticator, accounts directory.Accounts, hostname string) *Server {
	return &Server{
		auth:     auth,
		accounts: accounts,
		hostname: hostname,
		subs:     make(map[string]*ewsSubscription),
	}
}

// icsSync logs an ICS synchronization under the ics subsystem; EWS folder and item
// sync are ICS-backed. The client address rides on the correlated operation event.
func (s *Server) icsSync(user, scope string) {
	s.Logger.Emit(logging.Event{Level: logging.LevelInfo, Subsystem: logging.ICS, Name: "sync", User: user, Fields: logging.Fields{"scope": scope}})
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

// Package mapihttp serves a mailbox over the native Outlook transports: MAPI/HTTP
// ([MS-OXCMAPIHTTP]) — the EMSMDB endpoint on /mapi/emsmdb (the store/ROP channel)
// and the NSPI endpoint on /mapi/nspi (the address book) — and RPC-over-HTTP
// ([MS-RPCH], "Outlook Anywhere") on /rpc/rpcproxy.dll, which carries the same ROP
// and address-book calls over a DCE/RPC tunnel. It authenticates each request with
// HTTP Basic against the directory — modern Outlook over either transport accepts
// Basic, so no NTLM subsystem is required.
//
// This package owns the transport: routing, request-type dispatch, the session
// cookies, and the response framing. The ROP buffer carried inside Execute and
// the NSPI calls are decoded by their own layers; v1 targets online-mode mail.
package mapihttp

import (
	"context"
	"net/http"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/notify"
	"hermex/internal/nspi"
	"hermex/internal/relay"
	"hermex/internal/rpchttp"
	"hermex/internal/serve"
)

// Server answers MAPI/HTTP EMSMDB and NSPI requests, and RPC/HTTP requests, for
// authenticated users.
type Server struct {
	auth          directory.Authenticator
	accounts      directory.Accounts
	spool         *relay.Spool // outbound relay queue for submitted mail; nil sends local-only
	hostname      string
	sessions      *sessionStore
	nsp           *nspi.Server
	nspiSessions  *nspiSessionStore
	rpc           *rpchttp.Server
	async         *rpchttp.AsyncEMSMDB // the EcDoAsyncWaitEx long-poll stub; SetNotify wires its wake source
	notifyWait    time.Duration        // how long a NotificationWait long-poll holds before reporting "no events"
	notifyCadence time.Duration        // how often that long-poll re-checks the shared store
	waker         notify.Registrar     // push wake source; nil keeps the long-poll on its cadence only
	logger        *logging.Logger      // central activity log; nil disables logging
	ems           *rpchttp.EMSMDB      // the RPC/HTTP EMSMDB stub, held so SetLogger reaches its ROP sessions
}

// Session reclamation. A client that dies without Disconnect (or Unbind) leaves
// its session behind, and an EMSMDB session pins a ROP handle table with an open
// mailbox store, so the server would accumulate them for the life of the process.
// sessionTTL is how long a session may go untouched before it is reclaimed; it is
// far above the notification long-poll window (notifyWaitInterval), and the poll
// stamps the session when it parks, so a wait in progress is never reclaimed.
//
// Idle reclamation never reaches a client that keeps polling, so sessionMaxAge
// caps a session's absolute lifetime as well: past it the session is refused and
// swept however busy it has been, which bounds the ROP handle table and the open
// mailbox store a single long-running client can pin. A day is long enough that
// a working Outlook rebuilds its session about once between sittings rather than
// mid-task.
const (
	sessionTTL           = 30 * time.Minute
	sessionMaxAge        = 24 * time.Hour
	sessionSweepInterval = time.Minute
)

// RunSessionMaintenance reclaims idle EMSMDB and NSPI sessions every
// sessionSweepInterval until ctx is cancelled. A daemon starts it alongside
// serving; a server that never starts it keeps every session, which is what the
// tests want.
func (s *Server) RunSessionMaintenance(ctx context.Context) {
	t := time.NewTicker(sessionSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			if n := s.sessions.sweep(now, sessionTTL) + s.nspiSessions.sweep(now, sessionTTL); n > 0 {
				s.logger.Info(logging.MAPI, "session.reclaim", logging.Fields{"count": n})
			}
		}
	}
}

// SetLogger installs the central activity log. It fans the logger out to the
// RPC/HTTP EMSMDB stub as well, because both transports mint ROP sessions and a
// permission change or an access denial must be recorded whichever one carried
// it. A daemon calls this once before serving; nil disables logging.
func (s *Server) SetLogger(l *logging.Logger) {
	s.logger = l
	s.ems.Logger = l
}

// mapiEvent logs a MAPI/HTTP operation tagged with the client address (and, when
// known, the user). A nil logger is a no-op.
func (s *Server) mapiEvent(r *http.Request, level logging.Level, sub logging.Subsystem, name, user string, f logging.Fields) {
	s.logger.Emit(logging.Event{Level: level, Subsystem: sub, Name: name, User: user, RemoteAddr: serve.ClientAddr(r), Fields: f})
}

// NewServer builds a MAPI/HTTP server backed by the directory for authentication.
// The NSPI address book runs over the directory GAL when the directory can
// enumerate users (accounts implements directory.GAL); otherwise the GAL is
// empty. accounts also serves ROP recipient resolution.
func NewServer(auth directory.Authenticator, accounts directory.Accounts, hostname string, spool *relay.Spool) *Server {
	var gal directory.GAL
	if g, ok := accounts.(directory.GAL); ok {
		gal = g
	}
	// A process-stable GUID identifies this server instance to NSPI clients for
	// the lifetime of a binding; a restart re-mints it and clients re-bind.
	serverGUID, _ := mapi.ParseGUID(newGUID())
	s := &Server{
		auth:          auth,
		accounts:      accounts,
		spool:         spool,
		hostname:      hostname,
		sessions:      newSessionStore(sessionMaxAge),
		nsp:           nspi.NewServer(gal, serverGUID),
		nspiSessions:  newNspiSessionStore(),
		notifyWait:    notifyWaitInterval,
		notifyCadence: notifyPollCadence,
	}
	// RPC/HTTP (Outlook Anywhere) shares the directory and HTTP Basic auth; the
	// EMSMDB store interface and the NSPI address-book interface are registered on
	// its DCE/RPC dispatcher. NSPI reuses the same GAL-backed server the MAPI/HTTP
	// endpoint drives; the GAL is global to every authenticated caller, so the
	// adapter passes only the transport session's identity through to central
	// logging (the dispatch itself needs no per-session state).
	ems := rpchttp.NewEMSMDB(accounts)
	s.ems = ems
	ems.Spool = spool // external recipients of RPC/HTTP submissions are relayed
	disp := rpchttp.NewDispatcher()
	disp.Register(rpchttp.EMSMDBUUID, rpchttp.EMSMDBVersion, ems.Handle)
	// The AsyncEMSMDB interface carries the EcDoAsyncWaitEx notification long-poll;
	// it shares the EMSMDB sessions, resolving its async handle to one by the GUID.
	// Held on the server so SetNotify can wire its push wake source.
	s.async = rpchttp.NewAsyncEMSMDB(ems)
	disp.Register(rpchttp.AsyncEMSMDBUUID, rpchttp.AsyncEMSMDBVersion, s.async.Handle)
	disp.Register(nspi.RPCInterfaceUUID, nspi.RPCInterfaceVersion, func(sess *rpchttp.Session, opnum uint16, stub []byte) ([]byte, uint32) {
		out, fault := s.nsp.DispatchRPC(opnum, stub)
		user, addr := "", ""
		if sess != nil {
			user, addr = sess.User, sess.RemoteAddr
		}
		// Mirror the MAPI/HTTP NSPI path (see nspi.go): a per-op "operation" event at
		// debug, escalated to warn on an RPC fault. This fills the gap on the RPC/HTTP
		// (Outlook Anywhere) transport, whose shared /rpc POST hides the NSPI op from
		// the HTTP access log.
		level := logging.LevelDebug
		f := logging.Fields{"op": nspi.OperationName(opnum)}
		if fault != 0 {
			level, f["fault"] = logging.LevelWarn, fault
		}
		s.logger.Emit(logging.Event{
			Level: level, Subsystem: logging.NSPI, Name: "operation",
			User: user, RemoteAddr: addr, Fields: f,
		})
		return out, fault
	})
	// The address-book referral interface points a client at this host as its
	// directory server, so a desktop Outlook that queries it first finds the NSPI
	// endpoint here.
	disp.Register(rpchttp.RFRUUID, rpchttp.RFRVersion, rpchttp.NewRFR(hostname).Handle)
	s.rpc = rpchttp.NewServer(rpchttp.Config{Auth: s.basicAuth, Dispatch: disp.Dispatch})
	return s
}

// SetNotify wires the push wake source into both notification long-polls (MAPI/HTTP
// NotificationWait and RPC/HTTP EcDoAsyncWaitEx), so a mailbox change wakes a parked
// client the instant it lands instead of on the next cadence poll. A nil consumer
// (push disabled) leaves both on their cadence — the degradation floor. The daemon
// calls this once at startup, before serving.
func (s *Server) SetNotify(c *notify.Consumer) {
	if c == nil {
		return
	}
	s.waker = c
	s.async.SetWaker(c)
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
	case strings.HasPrefix(p, "/rpc/rpcproxy.dll"), strings.HasPrefix(p, "/rpcwithcert/rpcproxy.dll"):
		s.mapiEvent(r, logging.LevelDebug, logging.RPC, "request", "", logging.Fields{"path": r.URL.Path})
		s.rpc.ServeHTTP(w, r)
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

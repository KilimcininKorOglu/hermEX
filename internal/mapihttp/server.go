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
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/nspi"
	"hermex/internal/rpchttp"
)

// Server answers MAPI/HTTP EMSMDB and NSPI requests, and RPC/HTTP requests, for
// authenticated users.
type Server struct {
	auth         directory.Authenticator
	accounts     directory.Accounts
	hostname     string
	sessions     *sessionStore
	nsp          *nspi.Server
	nspiSessions *nspiSessionStore
	rpc          *rpchttp.Server
}

// NewServer builds a MAPI/HTTP server backed by the directory for authentication.
// The NSPI address book runs over the directory GAL when the directory can
// enumerate users (accounts implements directory.GAL); otherwise the GAL is
// empty. accounts also serves ROP recipient resolution.
func NewServer(auth directory.Authenticator, accounts directory.Accounts, hostname string) *Server {
	var gal directory.GAL
	if g, ok := accounts.(directory.GAL); ok {
		gal = g
	}
	// A process-stable GUID identifies this server instance to NSPI clients for
	// the lifetime of a binding; a restart re-mints it and clients re-bind.
	serverGUID, _ := mapi.ParseGUID(newGUID())
	s := &Server{
		auth:         auth,
		accounts:     accounts,
		hostname:     hostname,
		sessions:     newSessionStore(),
		nsp:          nspi.NewServer(gal, serverGUID),
		nspiSessions: newNspiSessionStore(),
	}
	// RPC/HTTP (Outlook Anywhere) shares the directory and HTTP Basic auth; the
	// EMSMDB store interface and the NSPI address-book interface are registered on
	// its DCE/RPC dispatcher. NSPI reuses the same GAL-backed server the MAPI/HTTP
	// endpoint drives; the adapter discards the transport session because the GAL
	// is global to every authenticated caller.
	ems := rpchttp.NewEMSMDB(accounts)
	disp := rpchttp.NewDispatcher()
	disp.Register(rpchttp.EMSMDBUUID, rpchttp.EMSMDBVersion, ems.Handle)
	disp.Register(nspi.RPCInterfaceUUID, nspi.RPCInterfaceVersion, func(_ *rpchttp.Session, opnum uint16, stub []byte) ([]byte, uint32) {
		return s.nsp.DispatchRPC(opnum, stub)
	})
	s.rpc = rpchttp.NewServer(rpchttp.Config{Auth: s.basicAuth, Dispatch: disp.Dispatch})
	return s
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

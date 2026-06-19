// Package activesync serves a mailbox over Exchange ActiveSync (EAS): the
// MS-ASHTTP transport on /Microsoft-Server-ActiveSync, the MS-ASCMD command set
// encoded as MS-ASWBXML (internal/wbxml), and the mobilesync Autodiscover
// endpoint. It authenticates each request with HTTP Basic against the directory
// and operates on the MAPI object store, reusing the same infrastructure as the
// IMAP and DAV daemons. v1 targets protocol 14.1 and the mail (Email) class.
package activesync

import (
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/serve"
)

// Server answers ActiveSync and Autodiscover requests for authenticated users.
type Server struct {
	auth     directory.Authenticator
	accounts directory.Accounts
	hostname string
	Logger   *logging.Logger // central activity log; nil disables logging
}

// NewServer builds an ActiveSync server backed by the directory for
// authentication and recipient resolution (the latter used by SendMail).
func NewServer(auth directory.Authenticator, accounts directory.Accounts, hostname string) *Server {
	return &Server{auth: auth, accounts: accounts, hostname: hostname}
}

// Handler returns the HTTP handler. One handler routes the two EAS paths by a
// case-insensitive match, since clients vary the casing of both.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.route)
	return mux
}

// route dispatches the ActiveSync endpoint and the Autodiscover endpoint.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch strings.ToLower(r.URL.Path) {
	case "/microsoft-server-activesync":
		s.serveActiveSync(w, r)
	case "/autodiscover/autodiscover.xml":
		s.serveAutodiscover(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveActiveSync authenticates, answers OPTIONS with the capability headers,
// and dispatches a POST command. Every method on this endpoint is authenticated
// (including OPTIONS), matching Exchange behaviour.
func (s *Server) serveActiveSync(w http.ResponseWriter, r *http.Request) {
	user, mailbox, ok := s.basicAuth(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodOptions {
		s.handleOptions(w)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "OPTIONS, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := parseQuery(r)
	if err != nil {
		http.Error(w, "bad ActiveSync query: "+err.Error(), http.StatusBadRequest)
		return
	}
	sess := &session{
		user:     user,
		mailbox:  mailbox,
		req:      req,
		protocol: protocolVersion(r),
	}
	s.dispatch(w, r, sess)
}

// session carries the per-request context handed to a command handler.
type session struct {
	user     string
	mailbox  string
	req      asRequest
	protocol string
}

// dispatch routes a parsed command to its handler. Command handlers are added
// per increment; an unrecognized or not-yet-implemented command returns 501.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, sess *session) {
	s.Logger.Emit(logging.Event{
		Level:      logging.LevelInfo,
		Subsystem:  logging.ActiveSync,
		Name:       "command",
		User:       sess.user,
		RemoteAddr: serve.ClientAddr(r),
		Fields:     logging.Fields{"cmd": sess.req.cmd},
	})
	switch sess.req.cmd {
	case "Provision":
		s.handleProvision(w, r)
	case "FolderSync":
		s.handleFolderSync(w, r, sess)
	case "Sync":
		s.handleSync(w, r, sess)
	case "SendMail", "SmartReply", "SmartForward":
		s.handleSendMail(w, r, sess)
	case "GetItemEstimate":
		s.handleGetItemEstimate(w, r, sess)
	case "Ping":
		s.handlePing(w, r, sess)
	case "Settings":
		s.handleSettings(w, r, sess)
	default:
		http.Error(w, "command not implemented: "+sess.req.cmd, http.StatusNotImplemented)
	}
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

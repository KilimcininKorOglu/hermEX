// Package activesync serves a mailbox over Exchange ActiveSync (EAS): the
// MS-ASHTTP transport on /Microsoft-Server-ActiveSync, the MS-ASCMD command set
// encoded as MS-ASWBXML (internal/wbxml), and the mobilesync Autodiscover
// endpoint. It authenticates each request with HTTP Basic against the directory
// and operates on the MAPI object store, reusing the same infrastructure as the
// IMAP and DAV daemons. v1 targets protocol 14.1 and the mail (Email) class.
package activesync

import (
	"crypto/x509"
	"net/http"
	"strings"
	"time"

	"hermex/internal/authlimit"
	"hermex/internal/directory"
	"hermex/internal/easpolicy"
	"hermex/internal/logging"
	"hermex/internal/notify"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
	"hermex/internal/serve"
)

// Server answers ActiveSync and Autodiscover requests for authenticated users.
type Server struct {
	auth     directory.Authenticator
	accounts directory.Accounts
	hostname string
	Logger   *logging.Logger    // central activity log; nil disables logging
	Limiter  *authlimit.Limiter // failed-login throttle keyed by account; nil disables it
	Spool    *relay.Spool       // outbound relay queue; nil sends local-only
	roots    *x509.CertPool     // S/MIME trust anchors for ValidateCert; nil = system roots
	Sessions SessionRecorder    // live-session telemetry sink; nil disables it
	waker    notify.Registrar   // push wake source; nil keeps Ping on its poll cadence only
}

// SetNotify wires the push wake source so a held Ping wakes the instant the
// mailbox changes rather than on its next cadence poll. A nil consumer (push
// disabled) leaves Ping on its cadence, the degradation floor. The daemon calls
// this once at startup, before serving.
func (s *Server) SetNotify(c *notify.Consumer) {
	if c == nil {
		return
	}
	s.waker = c
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

// failRequest logs the full internal error server-side and returns only a
// generic message to the client, so raw Go error strings (file paths, library
// internals, call-stack detail) never reach the wire. A 5xx logs at error, a
// 4xx (client fault) at warn. clientMsg is the sanitized text the client sees.
func (s *Server) failRequest(w http.ResponseWriter, r *http.Request, op string, err error, status int, clientMsg string) {
	level := logging.LevelWarn
	if status >= http.StatusInternalServerError {
		level = logging.LevelError
	}
	s.Logger.Emit(logging.Event{
		Level:      level,
		Subsystem:  logging.ActiveSync,
		Name:       op,
		RemoteAddr: serve.ClientAddr(r),
		Err:        err.Error(),
	})
	http.Error(w, clientMsg, status)
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
		s.failRequest(w, r, "query.parse.fail", err, http.StatusBadRequest, "bad ActiveSync query")
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

	tel   directory.SessionRecord // live-session telemetry row for this request
	telOn bool                    // whether telemetry is being recorded
}

// dispatch routes a parsed command to its handler. Command handlers are added
// per increment; an unrecognized or not-yet-implemented command returns 501.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, sess *session) {
	s.beginSession(r, sess)
	defer s.finishSession(sess)
	s.Logger.Emit(logging.Event{
		Level:      logging.LevelInfo,
		Subsystem:  logging.ActiveSync,
		Name:       "command",
		User:       sess.user,
		RemoteAddr: serve.ClientAddr(r),
		Fields:     logging.Fields{"cmd": sess.req.cmd},
	})
	if s.forceProvision(w, r, sess) {
		return
	}
	handler, ok := easCommands[sess.req.cmd]
	if !ok {
		http.Error(w, "command not implemented: "+sess.req.cmd, http.StatusNotImplemented)
		return
	}
	handler(s, w, r, sess)
}

// easCommands is the single routing source from an ActiveSync command name to its
// handler. A name absent here is a command this server does not implement.
var easCommands = map[string]func(*Server, http.ResponseWriter, *http.Request, *session){
	"Provision":         (*Server).handleProvision,
	"FolderSync":        (*Server).handleFolderSync,
	"Sync":              (*Server).handleSync,
	"MeetingResponse":   (*Server).handleMeetingResponse,
	"SendMail":          (*Server).handleSendMail,
	"SmartReply":        (*Server).handleSendMail,
	"SmartForward":      (*Server).handleSendMail,
	"GetItemEstimate":   (*Server).handleGetItemEstimate,
	"Ping":              (*Server).handlePing,
	"Settings":          (*Server).handleSettings,
	"ItemOperations":    (*Server).handleItemOperations,
	"MoveItems":         (*Server).handleMoveItems,
	"FolderCreate":      (*Server).handleFolderCreate,
	"FolderDelete":      (*Server).handleFolderDelete,
	"FolderUpdate":      (*Server).handleFolderUpdate,
	"ResolveRecipients": (*Server).handleResolveRecipients,
	"Search":            (*Server).handleSearch,
	"Find":              (*Server).handleFind,
	"ValidateCert":      (*Server).handleValidateCert,
}

// forceProvision stamps the calling device and answers HTTP 449 when the command
// must not run until the device provisions again. It reports whether the response
// is already written.
func (s *Server) forceProvision(w http.ResponseWriter, r *http.Request, sess *session) bool {
	var wipeStatus int
	if sess.req.deviceID != "" {
		wipeStatus = s.recordDevice(r, sess)
	}
	if sess.req.cmd == "Provision" {
		return false
	}
	// A pending remote wipe is delivered only through a Provision exchange, so any
	// other command from a device awaiting a wipe is answered with HTTP 449, which
	// forces the device to re-provision and pick the wipe up immediately.
	if wipeOutstanding(wipeStatus) {
		s.refuseUnprovisioned(w, r, sess, "")
		return true
	}
	// A configured device policy must be acknowledged: a command carrying a stale or
	// missing policy key is answered with 449, forcing the device to re-provision and
	// apply the current policy, this is how a policy change reaches an already-enrolled
	// device. A mailbox with no policy resolves to the baseline key "1" and requires no
	// provisioning, so unconfigured deployments never churn. (This resolves the policy per
	// command; a generation cache is a future optimization if it ever matters.)
	if sess.req.deviceID == "" {
		return false
	}
	want := easpolicy.Key(s.devicePolicy(sess))
	if want == "1" || sess.req.policyKey == want {
		return false
	}
	s.refuseUnprovisioned(w, r, sess, "stale-policy-key")
	return true
}

// refuseUnprovisioned answers the 449 that sends a device back through Provision.
func (s *Server) refuseUnprovisioned(w http.ResponseWriter, r *http.Request, sess *session, reason string) {
	fields := logging.Fields{"device": sess.req.deviceID, "cmd": sess.req.cmd}
	if reason != "" {
		fields["reason"] = reason
	}
	s.Logger.Emit(logging.Event{
		Level:      logging.LevelInfo,
		Subsystem:  logging.ActiveSync,
		Name:       "provision.force",
		User:       sess.user,
		RemoteAddr: serve.ClientAddr(r),
		Fields:     fields,
	})
	w.WriteHeader(449)
}

// basicAuth validates HTTP Basic credentials against the directory and returns
// the user and mailbox path. On failure it writes a 401 challenge.
func (s *Server) basicAuth(w http.ResponseWriter, r *http.Request) (user, mailbox string, ok bool) {
	u, p, hasAuth := r.BasicAuth()
	if hasAuth {
		// Throttle online guessing: an account that has piled up failed logins, or
		// the address they came from, is refused before the password is checked,
		// which also stops the 600k-round hash from running. The address axis reads
		// the forwarded client rather than the raw connection, since behind the
		// front door every request otherwise carries the proxy's own address.
		addr := serve.ClientAddr(r)
		if s.Limiter != nil && !s.Limiter.Allowed(addr, u) {
			s.logThrottled(r, u)
			http.Error(w, "too many failed attempts, try again later", http.StatusTooManyRequests)
			return "", "", false
		}
		if path, good := directory.AuthenticateClient(s.auth, u, p); good {
			// The credentials were right, so the attempt is a success even when the
			// service privilege then refuses it.
			if s.Limiter != nil {
				s.Limiter.Succeed(addr, u)
			}
			if privs, _ := s.auth.Privileges(u); !privs.EAS {
				http.Error(w, "ActiveSync access is disabled for this account", http.StatusForbidden)
				return "", "", false
			}
			return u, path, true
		}
		if s.Limiter != nil {
			s.Limiter.Fail(addr, u)
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="hermEX"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return "", "", false
}

// logThrottled records a refused-before-checked login so the operator sees a
// guessing wave rather than silence.
func (s *Server) logThrottled(r *http.Request, user string) {
	if s.Logger == nil {
		return
	}
	s.Logger.Emit(logging.Event{
		Level: logging.LevelWarn, Subsystem: logging.ActiveSync, Name: "auth.throttled",
		User: user, RemoteAddr: serve.ClientAddr(r),
	})
}

// recordDevice stamps the calling device's metadata (type, agent, negotiated
// version, last-seen time) for the management console's mobile-devices view and
// returns the device's current remote-wipe status so dispatch can force a
// pending wipe. Best-effort: it opens its own store handle and writes a sibling
// property apart from the sync-state blob, so a failure here is logged and never
// affects the command response. Skipped by the caller when the request carries
// no device id.
func (s *Server) recordDevice(r *http.Request, sess *session) int {
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		return WipeStatusUnknown
	}
	defer st.Close()
	status, err := recordDeviceContact(st, sess.req.deviceID, sess.user, sess.req.deviceType,
		r.Header.Get("User-Agent"), sess.protocol, time.Now().Unix())
	if err != nil {
		s.Logger.Emit(logging.Event{
			Level:      logging.LevelDebug,
			Subsystem:  logging.ActiveSync,
			Name:       "device.record.fail",
			User:       sess.user,
			RemoteAddr: serve.ClientAddr(r),
			Fields:     logging.Fields{"device": sess.req.deviceID, "error": err.Error()},
		})
		return WipeStatusUnknown
	}
	return status
}

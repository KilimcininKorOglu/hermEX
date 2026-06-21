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

	"hermex/internal/directory"
	"hermex/internal/easpolicy"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
	"hermex/internal/serve"
)

// Server answers ActiveSync and Autodiscover requests for authenticated users.
type Server struct {
	auth     directory.Authenticator
	accounts directory.Accounts
	hostname string
	Logger   *logging.Logger // central activity log; nil disables logging
	Spool    *relay.Spool    // outbound relay queue; nil sends local-only
	roots    *x509.CertPool  // S/MIME trust anchors for ValidateCert; nil = system roots
	Sessions SessionRecorder // live-session telemetry sink; nil disables it
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
	var wipeStatus int
	if sess.req.deviceID != "" {
		wipeStatus = s.recordDevice(r, sess)
	}
	// A pending remote wipe is delivered only through a Provision exchange, so any
	// other command from a device awaiting a wipe is answered with HTTP 449, which
	// forces the device to re-provision and pick the wipe up immediately.
	if sess.req.cmd != "Provision" && wipeOutstanding(wipeStatus) {
		s.Logger.Emit(logging.Event{
			Level:      logging.LevelInfo,
			Subsystem:  logging.ActiveSync,
			Name:       "provision.force",
			User:       sess.user,
			RemoteAddr: serve.ClientAddr(r),
			Fields:     logging.Fields{"device": sess.req.deviceID, "cmd": sess.req.cmd},
		})
		w.WriteHeader(449)
		return
	}
	// A configured device policy must be acknowledged: a non-Provision command carrying a
	// stale or missing policy key is answered with 449, forcing the device to re-provision
	// and apply the current policy — this is how a policy change reaches an already-enrolled
	// device. A mailbox with no policy resolves to the baseline key "1" and requires no
	// provisioning, so unconfigured deployments never churn. (This resolves the policy per
	// command; a generation cache is a future optimization if it ever matters.)
	if sess.req.cmd != "Provision" && sess.req.deviceID != "" {
		if want := easpolicy.Key(s.devicePolicy(sess)); want != "1" && sess.req.policyKey != want {
			s.Logger.Emit(logging.Event{
				Level:      logging.LevelInfo,
				Subsystem:  logging.ActiveSync,
				Name:       "provision.force",
				User:       sess.user,
				RemoteAddr: serve.ClientAddr(r),
				Fields:     logging.Fields{"device": sess.req.deviceID, "cmd": sess.req.cmd, "reason": "stale-policy-key"},
			})
			w.WriteHeader(449)
			return
		}
	}
	switch sess.req.cmd {
	case "Provision":
		s.handleProvision(w, r, sess)
	case "FolderSync":
		s.handleFolderSync(w, r, sess)
	case "Sync":
		s.handleSync(w, r, sess)
	case "MeetingResponse":
		s.handleMeetingResponse(w, r, sess)
	case "SendMail", "SmartReply", "SmartForward":
		s.handleSendMail(w, r, sess)
	case "GetItemEstimate":
		s.handleGetItemEstimate(w, r, sess)
	case "Ping":
		s.handlePing(w, r, sess)
	case "Settings":
		s.handleSettings(w, r, sess)
	case "ItemOperations":
		s.handleItemOperations(w, r, sess)
	case "MoveItems":
		s.handleMoveItems(w, r, sess)
	case "FolderCreate":
		s.handleFolderCreate(w, r, sess)
	case "FolderDelete":
		s.handleFolderDelete(w, r, sess)
	case "FolderUpdate":
		s.handleFolderUpdate(w, r, sess)
	case "ResolveRecipients":
		s.handleResolveRecipients(w, r, sess)
	case "Search":
		s.handleSearch(w, r, sess)
	case "ValidateCert":
		s.handleValidateCert(w, r, sess)
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
			if privs, _ := s.auth.Privileges(u); !privs.EAS {
				http.Error(w, "ActiveSync access is disabled for this account", http.StatusForbidden)
				return "", "", false
			}
			return u, path, true
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="hermEX"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return "", "", false
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

package serve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"hermex/internal/logging"
)

// userKey addresses the per-request identity slot in the request context.
type userKey struct{}

// requestUser is the mutable identity slot logMiddleware installs in every
// request context. A handler that authenticates from a cookie or a request body
// has no Basic header for the middleware to read, so it writes the account here
// instead. The mutex guards the handler's write against the middleware's read,
// which happens once the handler returns.
type requestUser struct {
	mu   sync.Mutex
	name string
}

func (u *requestUser) set(name string) {
	u.mu.Lock()
	u.name = name
	u.mu.Unlock()
}

func (u *requestUser) get() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.name
}

// SetUser records the account a request acts as, so the access log can attribute
// it. Daemons that authenticate with HTTP Basic need not call it (the middleware
// reads the header itself); the cookie-authenticated ones do, since nothing in
// the request otherwise names the caller. It is a no-op when the request never
// passed through the logging middleware, which is the case for an unconfigured
// daemon and for a test driving a handler directly.
func SetUser(r *http.Request, user string) {
	if u, ok := r.Context().Value(userKey{}).(*requestUser); ok {
		u.set(user)
	}
}

// logMiddleware wraps h to emit one structured Event per HTTP request through
// logger, tagged with subsystem. Each event records the method, path, response
// status, and duration, plus the client address (the first X-Forwarded-For hop
// when a proxy set it, so a request through the gateway logs the real client
// rather than the proxy), the acting account (never the password), and a request
// id (an inbound X-Request-Id when set, otherwise a freshly minted one).
// A nil logger leaves h unwrapped, so an unconfigured daemon pays nothing.
//
// The account comes from the presented HTTP Basic user, or, when the handler
// reported one through SetUser, from that: a handler that authenticated the
// caller itself knows the identity the Basic header cannot carry.
func logMiddleware(h http.Handler, logger *logging.Logger, subsystem logging.Subsystem) http.Handler {
	if logger == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = newRequestID()
		}
		acting := &requestUser{}
		r = r.WithContext(context.WithValue(r.Context(), userKey{}, acting))
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		user, _, _ := r.BasicAuth()
		if reported := acting.get(); reported != "" {
			user = reported
		}
		logger.Emit(logging.Event{
			Level:      levelForStatus(rec.status),
			Subsystem:  subsystem,
			Name:       "http.request",
			User:       user,
			RemoteAddr: ClientAddr(r),
			RequestID:  rid,
			DurationMs: time.Since(start).Milliseconds(),
			Fields: logging.Fields{
				"method": r.Method,
				"path":   r.URL.Path,
				"status": rec.status,
			},
		})
	})
}

// statusRecorder wraps a ResponseWriter to capture the response status, defaulting
// to 200 for handlers that write a body without an explicit WriteHeader. It
// forwards Flush so streaming handlers (e.g. MAPI/HTTP chunked responses) keep
// working through the wrapper.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

// WriteHeader records the first status written and forwards it; http honors only
// the first WriteHeader, so the first is the response's real status.
func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer when it supports flushing, so a wrapped
// handler can still stream.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// levelForStatus maps an HTTP status to a log level: 5xx is an error, 4xx a
// warning, everything else informational.
func levelForStatus(status int) logging.Level {
	switch {
	case status >= 500:
		return logging.LevelError
	case status >= 400:
		return logging.LevelWarn
	default:
		return logging.LevelInfo
	}
}

// ClientAddr returns the originating client address: the first X-Forwarded-For hop
// when a proxy (the gateway) set it, otherwise the connection's RemoteAddr. It is
// exported so the protocol packages behind the gateway log the real client too.
func ClientAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	return r.RemoteAddr
}

// newRequestID mints a short random hex id for correlating an access-log line with
// the rest of a request's events. Identifiers are best effort: a (vanishingly
// unlikely) RNG failure yields an empty id rather than failing the request.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

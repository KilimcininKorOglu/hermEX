package serve

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"hermex/internal/logging"
)

// recordSink keeps every event the middleware emits.
type recordSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (c *recordSink) Write(e logging.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *recordSink) last() (logging.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return logging.Event{}, false
	}
	return c.events[len(c.events)-1], true
}

// serveThrough runs one request through the logging middleware and returns the
// recorded event.
func serveThrough(t *testing.T, h http.HandlerFunc, req *http.Request) logging.Event {
	t.Helper()
	sink := &recordSink{}
	logMiddleware(h, logging.New(sink), logging.Webmail).ServeHTTP(httptest.NewRecorder(), req)
	e, ok := sink.last()
	if !ok {
		t.Fatal("the middleware recorded no event")
	}
	return e
}

// TestLogUsesHandlerReportedUser proves a cookie-authenticated handler can name
// the acting account. Webmail and the admin panel authenticate from a cookie, so
// there is no Basic header to read, and without this the access log records their
// most sensitive mutations against no account at all.
func TestLogUsesHandlerReportedUser(t *testing.T) {
	e := serveThrough(t, func(w http.ResponseWriter, r *http.Request) {
		SetUser(r, "alice@hermex.test")
	}, httptest.NewRequest(http.MethodPost, "/api/v1/settings/password", nil))

	if e.User != "alice@hermex.test" {
		t.Errorf("user = %q, want the account the handler reported", e.User)
	}
}

// TestLogFallsBackToBasicUser proves the Basic-Auth daemons are unaffected: a
// handler that reports nothing still logs the presented Basic user.
func TestLogFallsBackToBasicUser(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ews/Exchange.asmx", nil)
	req.SetBasicAuth("bob@hermex.test", "secret")

	e := serveThrough(t, func(http.ResponseWriter, *http.Request) {}, req)

	if e.User != "bob@hermex.test" {
		t.Errorf("user = %q, want the presented Basic user", e.User)
	}
}

// TestLogPrefersReportedUserOverBasic proves a handler that authenticated the
// caller itself wins over the header: a presented Basic user is only a claim,
// while the reported one has been verified.
func TestLogPrefersReportedUserOverBasic(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail", nil)
	req.SetBasicAuth("claimed@hermex.test", "secret")

	e := serveThrough(t, func(w http.ResponseWriter, r *http.Request) {
		SetUser(r, "verified@hermex.test")
	}, req)

	if e.User != "verified@hermex.test" {
		t.Errorf("user = %q, want the handler-reported account", e.User)
	}
}

// TestSetUserWithoutMiddlewareIsSafe proves SetUser is inert off the middleware
// path, so a handler may call it unconditionally: an unconfigured daemon runs
// unwrapped, and a test can drive a handler directly.
func TestSetUserWithoutMiddlewareIsSafe(t *testing.T) {
	SetUser(httptest.NewRequest(http.MethodGet, "/", nil), "alice@hermex.test")
}

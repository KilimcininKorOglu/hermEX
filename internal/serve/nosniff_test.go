package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNosniffReachesTheWire is the wiring proof, and the one that matters: it goes
// through serve.New, which is what every HTTP daemon builds its listener with. The
// middleware being correct in isolation says nothing about whether any daemon
// actually carries it.
func TestNosniffReachesTheWire(t *testing.T) {
	base := startLimited(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		_, _ = w.Write([]byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"))
	}), nil)

	resp, err := http.Get(base + "dav/calendars/u/default/x.ics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("a served response carries X-Content-Type-Options %q, want nosniff", got)
	}
}

// TestNosniffOnEveryResponse is the defect. Only webmail2api stamped the header,
// in its own middleware, so every other daemon on this base served without it. The
// one that matters is DAV: a client PUTs a vCard or an iCalendar object and gets
// its own bytes back, and the gateway publishes /dav/ on the same origin as the
// webmail SPA. Without the header a browser is free to disregard text/vcard and
// render the stored bytes as HTML.
func TestNosniffOnEveryResponse(t *testing.T) {
	h := nosniffMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
		_, _ = w.Write([]byte("BEGIN:VCARD\r\nFN:<script>alert(1)</script>\r\nEND:VCARD\r\n"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dav/addressbooks/u/default/x.vcf", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/vcard; charset=utf-8" {
		t.Errorf("the declared Content-Type was altered: %q", ct)
	}
}

// TestNosniffSetBeforeTheBody proves the header is stamped on the way in, not after
// the handler writes. Headers set after the first Write never reach the client, so
// stamping it afterwards would look correct in the source and do nothing on the
// wire.
func TestNosniffSetBeforeTheBody(t *testing.T) {
	h := nosniffMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("body"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("a handler that commits its status first loses the header: %q", got)
	}
}

// TestNosniffDoesNotOverrideAHandlersOwnValue guards the composition with
// webmail2api, which stamps the same header in its own security middleware. Both
// set the identical value, so the result must still be a single well-formed header
// rather than a duplicated or concatenated one.
func TestNosniffDoesNotOverrideAHandlersOwnValue(t *testing.T) {
	h := nosniffMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if v := rec.Header().Values("X-Content-Type-Options"); len(v) != 1 || v[0] != "nosniff" {
		t.Errorf("X-Content-Type-Options = %v, want exactly one nosniff", v)
	}
}

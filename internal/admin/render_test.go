package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/logging"
)

// renderServer builds a server whose render failures are captured.
func renderServer(t *testing.T, sink logging.Sink) *Server {
	t.Helper()
	srv := NewServer(&fakeDir{}, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	if sink != nil {
		srv.SetLogger(logging.New(sink))
	}
	return srv
}

// brokenStatusData is the Live status page's model with Results holding something
// the template's range cannot walk. It is a realistic failure: the page writes its
// whole head, sidebar and table header before reaching the range, so a good chunk
// of the response exists by the time execution stops.
func brokenStatusData() map[string]any {
	return map[string]any{"Nav": "status", "Configured": true, "Results": 42}
}

// TestRenderFailureIsACleanError is the defect. Executing straight into the
// response writer commits the 200 and part of the body before a failure can be
// noticed, so the error branch could only append to a page already on the wire. The
// operator saw a silently truncated page under a stale 200, which for a management
// UI is the worst outcome: a half-rendered form misrepresents state.
func TestRenderFailureIsACleanError(t *testing.T) {
	srv := renderServer(t, nil)

	rec := httptest.NewRecorder()
	srv.render(rec, "status.html", brokenStatusData())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	// Everything before the range is what used to reach the client.
	for _, leaked := range []string{"Live status", "<table", "<html", "sidebar"} {
		if strings.Contains(body, leaked) {
			t.Errorf("the response carries the partial render (%q):\n%s", leaked, body)
		}
	}
	if !strings.Contains(body, "render error") {
		t.Errorf("the response does not say it failed:\n%s", body)
	}
	// A 500 whose content type still claims HTML invites the browser to render the
	// error text as a page.
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q on a failed render", ct)
	}
}

// TestRenderFailureIsRecorded proves the operator can find out why. The client
// response is deliberately opaque, so without this the cause exists nowhere.
func TestRenderFailureIsRecorded(t *testing.T) {
	sink := &failCaptureSink{}
	srv := renderServer(t, sink)

	srv.render(httptest.NewRecorder(), "status.html", brokenStatusData())

	e, ok := sink.find("render.fail")
	if !ok {
		t.Fatal("a failed render left no record, so the cause exists nowhere")
	}
	if e.Fields["template"] != "status.html" {
		t.Errorf("the record does not name the template: %v", e.Fields)
	}
	if e.Err == "" {
		t.Error("the record carries no cause")
	}
	if e.Level != logging.LevelError || e.Subsystem != logging.Admin {
		t.Errorf("event = %s/%s, want error/admin", e.Level, e.Subsystem)
	}
}

// TestRenderSucceedsUnchanged guards the other direction: buffering must not change
// what a working page returns.
func TestRenderSucceedsUnchanged(t *testing.T) {
	srv := renderServer(t, nil)
	rec := httptest.NewRecorder()
	srv.render(rec, "status.html", map[string]any{
		"Nav": "status", "Configured": false, "Results": []healthResult{},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Live status") || !strings.Contains(body, "No health targets configured") {
		t.Errorf("body is not the rendered page:\n%s", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

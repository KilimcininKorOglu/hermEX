package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"hermex/internal/config"
	"hermex/internal/logging"
)

// panicSink collects the events the recovery reports.
type panicSink struct{ events []logging.Event }

func (s *panicSink) Write(e logging.Event) { s.events = append(s.events, e) }

// servePanicking runs one request through the recovery around a handler that
// panics with v, and returns the recorder and the sink.
func servePanicking(v any) (*httptest.ResponseRecorder, *panicSink) {
	sink := &panicSink{}
	h := recoverMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(v)
	}), logging.New(sink), logging.EWS)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/EWS/Exchange.asmx", nil))
	return rec, sink
}

// lockedSink collects events from a served request, which runs on the server's own
// goroutine.
type lockedSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (s *lockedSink) Write(e logging.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *lockedSink) find(name string) (logging.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// TestAPanicReachesTheWireAs500 is the wiring proof, and the one that matters: it
// goes through serve.New, which is what every HTTP daemon builds its listener with.
// The middleware being correct in isolation says nothing about whether any daemon
// carries it, and without the wiring net/http drops the connection so the client
// reads an EOF instead of a status.
func TestAPanicReachesTheWireAs500(t *testing.T) {
	sink := &lockedSink{}
	hs, err := New("127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("a request the handler could not take")
	}), &config.Config{}, logging.New(sink), logging.EWS, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = hs.Start() }()
	t.Cleanup(func() { _ = hs.Shutdown(context.Background()) })

	resp, err := http.Get("http://" + hs.Addr().String() + "/EWS/Exchange.asmx")
	if err != nil {
		t.Fatalf("the client got no response at all: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if _, ok := sink.find("handler.panic"); !ok {
		t.Error("the panic was not recorded in the central log")
	}
}

// TestAPanickingHandlerAnswersAndIsRecorded is the load-bearing case: net/http
// recovers a handler panic itself, but it writes the trace to the server's own
// ErrorLog and drops the connection, so the operator's central log holds nothing
// and the client sees a reset instead of a status.
func TestAPanickingHandlerAnswersAndIsRecorded(t *testing.T) {
	rec, sink := servePanicking("a request the handler could not take")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if len(sink.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(sink.events))
	}
	e := sink.events[0]
	if e.Name != "handler.panic" {
		t.Errorf("event = %q, want handler.panic", e.Name)
	}
	if e.Level != logging.LevelError {
		t.Errorf("level = %v, want error", e.Level)
	}
	if !strings.Contains(e.Err, "could not take") {
		t.Errorf("event err = %q, want the panic value", e.Err)
	}
	if got, _ := e.Fields["path"].(string); got != "/EWS/Exchange.asmx" {
		t.Errorf("event path = %q, want the request path", got)
	}
	if stack, _ := e.Fields["stack"].(string); !strings.Contains(stack, "serve") {
		t.Errorf("event carries no usable stack: %q", stack)
	}
}

// TestAnOrdinaryHandlerIsUntouched is the negative control: the recovery must not
// change a request that did not panic.
func TestAnOrdinaryHandlerIsUntouched(t *testing.T) {
	sink := &panicSink{}
	h := recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("fine"))
	}), logging.New(sink), logging.EWS)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want the handler's own", rec.Code)
	}
	if rec.Body.String() != "fine" {
		t.Errorf("body = %q, want the handler's own", rec.Body.String())
	}
	if len(sink.events) != 0 {
		t.Errorf("recorded %d events for a request that did not panic", len(sink.events))
	}
}

// TestErrAbortHandlerIsLeftToNetHTTP keeps the documented way a handler asks
// net/http to drop a connection without logging: it must pass through, not become
// a 500 and a recorded fault.
func TestErrAbortHandlerIsLeftToNetHTTP(t *testing.T) {
	sink := &panicSink{}
	h := recoverMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}), logging.New(sink), logging.EWS)

	defer func() {
		v := recover()
		if v != http.ErrAbortHandler {
			t.Errorf("recovered %v, want ErrAbortHandler re-panicked", v)
		}
		if len(sink.events) != 0 {
			t.Errorf("recorded %d events for an aborted handler", len(sink.events))
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	t.Error("ErrAbortHandler was swallowed")
}

package admin

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"hermex/internal/logging"
)

// failCaptureSink records every event so a test can assert the internal error was
// kept rather than dropped.
type failCaptureSink struct {
	mu     sync.Mutex
	events []logging.Event
}

func (c *failCaptureSink) Write(e logging.Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *failCaptureSink) find(name string) (logging.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Name == name {
			return e, true
		}
	}
	return logging.Event{}, false
}

// loggingAdminServer builds a test server whose failures are captured, so a test can
// check both what the client was told and what was recorded.
func loggingAdminServer(t *testing.T, d Directory) (*httptest.Server, *failCaptureSink) {
	t.Helper()
	sink := &failCaptureSink{}
	srv := NewServer(d, fakePaths{root: t.TempDir()}, []byte("test-secret"))
	srv.store = &fakeStore{}
	srv.SetLogger(logging.New(sink))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, sink
}

// driverError is the shape of a real MariaDB failure: it names the table, the column
// and the constraint. Returning it to a client hands over part of the schema.
const driverError = `Error 1062 (23000): Duplicate entry 'bob@hermex.test' for key 'users.username'`

// TestCreateUserDoesNotLeakDriverText proves a directory failure is answered with a
// fixed message. An admin-panel account is not necessarily a system administrator, so
// driver text naming tables, columns and constraints is a schema map handed to a
// scoped account.
func TestCreateUserDoesNotLeakDriverText(t *testing.T) {
	d := systemAdminDir()
	d.createErr = errors.New(driverError)
	ts, _ := loggingAdminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users", session, csrf,
		`{"email":"bob@hermex.test","password":"pw"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with a directory error = %d, want 400: %s", resp.StatusCode, body)
	}
	for _, leak := range []string{"1062", "Duplicate entry", "users.username"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the response carries driver text %q: %s", leak, body)
		}
	}
	if !strings.Contains(string(body), "could not create user") {
		t.Errorf("the response dropped the handler's own message: %s", body)
	}
}

// TestCreateUserRecordsTheRealError proves the sanitized response does not swallow the
// failure: the full error reaches the central log, where an operator diagnoses it.
// Without this the fix would trade a leak for a blind spot.
func TestCreateUserRecordsTheRealError(t *testing.T) {
	d := systemAdminDir()
	d.createErr = errors.New(driverError)
	ts, sink := loggingAdminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users", session, csrf,
		`{"email":"bob@hermex.test","password":"pw"}`)
	resp.Body.Close()

	e, ok := sink.find("request.fail")
	if !ok {
		t.Fatal("the failure was not recorded, so the real error is lost")
	}
	if !strings.Contains(e.Err, "Duplicate entry") {
		t.Errorf("recorded error = %q, want the full driver text", e.Err)
	}
	if e.Subsystem != logging.Admin {
		t.Errorf("recorded subsystem = %q, want %q", e.Subsystem, logging.Admin)
	}
}

// TestFailMapsStatusToLevel proves a server fault is recorded at error level and a
// client fault at warn, so a 5xx is not buried among 4xx noise, and that neither
// spells the internal error out to the client.
func TestFailMapsStatusToLevel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   logging.Level
	}{
		{"server fault", http.StatusInternalServerError, logging.LevelError},
		{"client fault", http.StatusBadRequest, logging.LevelWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &failCaptureSink{}
			s := &Server{logger: logging.New(sink)}
			rec := httptest.NewRecorder()

			s.fail(rec, "could not save the thing", errors.New(driverError), tc.status)

			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if strings.Contains(rec.Body.String(), "Duplicate entry") {
				t.Errorf("the response carries driver text: %s", rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "could not save the thing") {
				t.Errorf("the response dropped the message: %s", rec.Body.String())
			}
			e, ok := sink.find("request.fail")
			if !ok {
				t.Fatal("the failure was not recorded")
			}
			if e.Level != tc.want {
				t.Errorf("recorded level = %v, want %v", e.Level, tc.want)
			}
		})
	}
}

// TestFailWithoutALoggerStillAnswers proves the panel serves when no logger is
// attached: the diagnosis is lost, the response is not.
func TestFailWithoutALoggerStillAnswers(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.fail(rec, "could not save the thing", errors.New(driverError), http.StatusInternalServerError)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Duplicate entry") {
		t.Errorf("the response carries driver text: %s", rec.Body.String())
	}
}

// TestSyncPolicyValidationStillNamesTheField proves the policy validator's own message
// is still returned. It is written by the policy package, names the offending field,
// and carries no driver or filesystem text, so sanitizing it would cost an API caller
// the only signal telling them what to fix.
func TestSyncPolicyValidationStillNamesTheField(t *testing.T) {
	ts, _ := loggingAdminServer(t, systemAdminDir())
	session, csrf := loginCookies(t, ts)

	resp := authedReq(t, ts, http.MethodPut, "/admin/syncpolicy", session, csrf, `{"NoSuchField":1}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown policy field = %d, want 400: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "NoSuchField") {
		t.Errorf("the validation message no longer names the offending field: %s", body)
	}
}

package dav

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"hermex/internal/logging"
)

// failingWriter is a ResponseWriter whose body write fails, as it does when the
// client hangs up after the status line was sent.
type failingWriter struct{ *httptest.ResponseRecorder }

var errClientGone = errors.New("client went away")

func (failingWriter) Write([]byte) (int, error) { return 0, errClientGone }

// TestALostResponseBodyIsRecorded sends a reply body the client never receives.
// The status is already on the wire, so nothing can be answered, but AGENTS.md
// requires a swallowed failure to be recorded: the operator must see a
// response.write.fail event carrying the write error, not a clean run.
func TestALostResponseBodyIsRecorded(t *testing.T) {
	srv := NewServer(nil, nil, "hermex.test")
	sink := &scheduleSink{}
	srv.Logger = logging.New(sink)

	w := failingWriter{httptest.NewRecorder()}
	srv.writeQuotaExceeded(w)

	if w.Code != http.StatusInsufficientStorage {
		t.Errorf("status = %d, want 507", w.Code)
	}
	var got *logging.Event
	for i := range sink.events {
		if sink.events[i].Name == "response.write.fail" {
			got = &sink.events[i]
		}
	}
	if got == nil {
		t.Fatalf("no response.write.fail event among %d events", len(sink.events))
	}
	if got.Err != errClientGone.Error() {
		t.Errorf("event error = %q, want %q", got.Err, errClientGone.Error())
	}
}

// TestADeliveredResponseBodyRecordsNoFailure sends a body that reaches the client;
// no write failure is recorded.
func TestADeliveredResponseBodyRecordsNoFailure(t *testing.T) {
	srv := NewServer(nil, nil, "hermex.test")
	sink := &scheduleSink{}
	srv.Logger = logging.New(sink)

	w := httptest.NewRecorder()
	srv.writeQuotaExceeded(w)

	if w.Body.Len() == 0 {
		t.Error("the quota-not-exceeded body was not written")
	}
	for _, e := range sink.events {
		if e.Name == "response.write.fail" {
			t.Errorf("a delivered body recorded %s: %s", e.Name, e.Err)
		}
	}
}

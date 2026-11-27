package serve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/logging"
)

// writeBody writes s as a handler body and fails the test when the write fails,
// so a broken response is not mistaken for a short one.
func writeBody(t *testing.T, w io.Writer, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s); err != nil {
		t.Errorf("write response body: %v", err)
	}
}

// runServer serves hs until the test ends. Cleanup shuts it down and requires the
// serve loop to end with http.ErrServerClosed, the result of a clean Shutdown;
// any other end means the server stopped by itself.
func runServer(t *testing.T, hs *Server) {
	t.Helper()
	served := make(chan error, 1)
	go func() { served <- hs.Start() }()
	t.Cleanup(func() {
		if err := hs.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve ended with %v, want http.ErrServerClosed", err)
		}
	})
}

// closeBody closes a client response body and fails the test when that fails.
func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// wantField fails the test unless a logged event carries the expected field.
func wantField(t *testing.T, e logging.Event, key string, want any, what string) {
	t.Helper()
	if e.Fields[key] != want {
		t.Errorf("%s = %v, want %v", what, e.Fields[key], want)
	}
}

// wantNoSecret fails the test when a password reached the rendered event, which
// is what an operator would actually see.
func wantNoSecret(t *testing.T, e logging.Event, secret string) {
	t.Helper()
	var rendered strings.Builder
	logging.NewStderrSink(&rendered).Write(e)
	if strings.Contains(rendered.String(), secret) {
		t.Errorf("the password %q leaked into the logged event", secret)
	}
}

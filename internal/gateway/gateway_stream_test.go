package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type lineResult struct {
	line string
	err  error
}

func readLineWithTimeout(t *testing.T, r *bufio.Reader, timeout time.Duration) string {
	t.Helper()
	ch := make(chan lineResult, 1)
	go func() {
		line, err := r.ReadString('\n')
		ch <- lineResult{line: line, err: err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("read chunk: %v", res.err)
		}
		return res.line
	case <-time.After(timeout):
		t.Fatal("chunk did not arrive incrementally, the gateway buffered the stream")
		return ""
	}
}

// TestGatewayForwardsChunkedIncrementally proves the front door streams a
// chunked (no Content-Length) backend response to the client incrementally,
// rather than buffering it until the body completes, the property EWS streaming
// notifications depend on. The backend blocks on a channel after the first chunk,
// so the test reading that chunk before releasing the second deterministically
// proves incremental forwarding (a buffering proxy would deadlock here, caught by
// the read deadline).
func TestGatewayForwardsChunkedIncrementally(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("backend ResponseWriter is not a Flusher")
			return
		}
		if _, err := io.WriteString(w, "chunk-one\n"); err != nil {
			t.Errorf("write chunk-one: %v", err)
			return
		}
		fl.Flush()
		<-release // hold the connection open (no Content-Length, body not done)
		if _, err := io.WriteString(w, "chunk-two\n"); err != nil {
			t.Errorf("write chunk-two: %v", err)
			return
		}
		fl.Flush()
	}))
	defer backend.Close()
	defer unblock()

	h, err := Handler([]Route{{Prefix: "/", Target: backend.URL}})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	resp, err := http.Get(front.URL + "/ews/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)

	// The first chunk must arrive while the backend is still blocked on release.
	reader := bufio.NewReader(resp.Body)
	first := readLineWithTimeout(t, reader, 2*time.Second)
	if !strings.Contains(first, "chunk-one") {
		t.Errorf("first chunk = %q, want chunk-one", first)
	}

	// Release the backend and confirm the second chunk streams through cleanly.
	unblock()
	second := readLineWithTimeout(t, reader, 2*time.Second)
	if !strings.Contains(second, "chunk-two") {
		t.Errorf("second chunk = %q, want chunk-two", second)
	}
}

// TestStreamRefusesUnroutedPathWithoutStartingBackend proves an unrouted or
// oversized request path returns 502 without invoking the streaming backend.
func TestStreamRefusesUnroutedPathWithoutStartingBackend(t *testing.T) {
	var streamCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streamCalls.Add(1)
		if _, err := io.WriteString(w, "chunk-one\n"); err != nil {
			t.Errorf("write stream chunk: %v", err)
		}
	}))
	defer backend.Close()

	h, err := Handler([]Route{{Prefix: "/ews/", Target: backend.URL}})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	for _, path := range []string{"/other/stream", "/ews", "/" + strings.Repeat("s", 4096)} {
		resp, err := http.Get(front.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		closeBody(t, resp)
		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("GET %s: status = %d, want 502", path, resp.StatusCode)
		}
		if got := streamCalls.Load(); got != 0 {
			t.Errorf("GET %s started backend stream (%d calls), want 0", path, got)
		}
	}

	resp, err := http.Get(front.URL + "/ews/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK || streamCalls.Load() != 1 {
		t.Errorf("valid stream: status = %d, calls = %d, want 200 and 1", resp.StatusCode, streamCalls.Load())
	}
}

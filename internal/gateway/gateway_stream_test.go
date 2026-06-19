package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestGatewayForwardsChunkedIncrementally proves the front door streams a
// chunked (no Content-Length) backend response to the client incrementally,
// rather than buffering it until the body completes — the property EWS streaming
// notifications depend on. The backend blocks on a channel after the first chunk,
// so the test reading that chunk before releasing the second deterministically
// proves incremental forwarding (a buffering proxy would deadlock here, caught by
// the read deadline).
func TestGatewayForwardsChunkedIncrementally(t *testing.T) {
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("backend ResponseWriter is not a Flusher")
			return
		}
		io.WriteString(w, "chunk-one\n")
		fl.Flush()
		<-release // hold the connection open (no Content-Length, body not done)
		io.WriteString(w, "chunk-two\n")
		fl.Flush()
	}))
	defer backend.Close()
	defer close(release)

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
	defer resp.Body.Close()

	// The first chunk must arrive while the backend is still blocked on release.
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if !strings.Contains(line, "chunk-one") {
			t.Errorf("first chunk = %q, want chunk-one", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk did not arrive incrementally — the gateway buffered the stream")
	}
}

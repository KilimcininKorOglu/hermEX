package serve

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/gzip"

	"hermex/internal/config"
	"hermex/internal/logging"
)

// jsonBody is a body large enough to clear the size floor and repetitive enough
// that compression is worth measuring, the shape a folder listing or a user
// directory actually has.
func jsonBody(entries int) string {
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":` + strconv.Itoa(i) + `,"name":"user` + strconv.Itoa(i) + `@hermex.test","role":"member"}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

// serveCompressed runs one request through the compression middleware and returns
// the recorder.
func serveCompressed(h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	compressMiddleware(h).ServeHTTP(rec, req)
	return rec
}

// gzipRequest builds a GET announcing gzip support.
func gzipRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/folders", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	return req
}

// TestCompressesLargeJSON proves a sizeable JSON response goes out gzipped and
// decodes back to exactly what the handler wrote. Nothing is worth compressing if
// the bytes do not survive the round trip.
func TestCompressesLargeJSON(t *testing.T) {
	body := jsonBody(200)
	rec := serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}, gzipRequest())

	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}
	if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
		t.Errorf("Vary = %q, want it to name Accept-Encoding", v)
	}
	if rec.Body.Len() >= len(body) {
		t.Errorf("compressed body is %d bytes against %d uncompressed; nothing was saved", rec.Body.Len(), len(body))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("the body is not a gzip stream: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Error("the decompressed body does not match what the handler wrote")
	}
}

// TestDoesNotCompressWithoutAcceptEncoding is the compatibility floor: a client
// that never announced gzip must receive exactly the bytes it would have before.
func TestDoesNotCompressWithoutAcceptEncoding(t *testing.T) {
	body := jsonBody(200)
	rec := serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}, httptest.NewRequest(http.MethodGet, "/api/v1/folders", nil))

	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none", enc)
	}
	if rec.Body.String() != body {
		t.Error("the body was altered for a client that asked for no encoding")
	}
}

// TestDoesNotCompressOtherTypes proves the Exchange-compatible surfaces and the
// already-compressed payloads are left alone. Those are wire contracts with real
// clients, and a second compression pass over an archive only adds bytes.
func TestDoesNotCompressOtherTypes(t *testing.T) {
	for _, ct := range []string{
		"text/xml; charset=utf-8", "application/vnd.ms-sync.wbxml",
		"application/zip", "image/png", "text/html; charset=utf-8",
	} {
		t.Run(ct, func(t *testing.T) {
			rec := serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", ct)
				io.WriteString(w, jsonBody(200))
			}, gzipRequest())
			if enc := rec.Header().Get("Content-Encoding"); enc != "" {
				t.Errorf("Content-Encoding = %q, want none for %s", enc, ct)
			}
		})
	}
}

// TestDoesNotCompressSmallJSON proves the size floor holds: below it the gzip
// framing costs more than it saves.
func TestDoesNotCompressSmallJSON(t *testing.T) {
	rec := serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}, gzipRequest())

	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none for a tiny body", enc)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Errorf("body = %q, want it untouched", rec.Body.String())
	}
}

// TestDoesNotDoubleEncode proves a handler that encoded its own body keeps its
// encoding rather than having a second one wrapped around it.
func TestDoesNotDoubleEncode(t *testing.T) {
	rec := serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "br")
		io.WriteString(w, jsonBody(200))
	}, gzipRequest())

	if enc := rec.Header().Get("Content-Encoding"); enc != "br" {
		t.Errorf("Content-Encoding = %q, want the handler's own br", enc)
	}
}

// TestStreamingResponseStillFlushes is the push regression: a compressor buffers,
// and buffering is what a server-sent-event channel cannot afford. A flushed
// event must reach the client before the response ends, or every long-poll
// consumer behind this middleware stops being a push channel.
func TestStreamingResponseStillFlushes(t *testing.T) {
	flushed, released := make(chan struct{}), make(chan struct{})
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		compressMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: first\n\n")
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("flush: %v", err)
			}
			close(flushed)
			<-released
		})).ServeHTTP(rec, gzipRequest())
	}()

	// The event must be readable while the handler is still parked. Reading after
	// the flush signal orders this against the handler's write.
	<-flushed
	if !strings.Contains(rec.Body.String(), "data: first") {
		t.Error("the flushed event never reached the recorder; the push channel was buffered")
	}
	close(released)
	<-done
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none for an event stream", enc)
	}
}

// TestPreservesStatusCode proves a non-200 answer keeps its status, so an error
// response is not turned into a 200 by the wrapper deferring the header.
func TestPreservesStatusCode(t *testing.T) {
	rec := serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"not found"}`)
	}, gzipRequest())

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestEmptyResponseIsUntouched proves a handler that writes no body, the shape of
// a 204, still answers with its own status and nothing else.
func TestEmptyResponseIsUntouched(t *testing.T) {
	rec := serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}, gzipRequest())

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
}

// TestServedDaemonCompresses proves the middleware is actually installed in the
// shared HTTP base, not merely written. Every daemon builds its server through
// New, so this is what makes the change reach webmail and the administration API.
func TestServedDaemonCompresses(t *testing.T) {
	body := jsonBody(200)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	})
	hs, err := New("127.0.0.1:0", h, &config.Config{}, nil, logging.Webmail, nil)
	if err != nil {
		t.Fatal(err)
	}
	go hs.Start()
	defer hs.Shutdown(context.Background())

	req, err := http.NewRequest(http.MethodGet, "http://"+hs.Addr().String()+"/api/v1/folders", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Set the header by hand: the stdlib transport would otherwise add gzip and
	// transparently decode it, hiding whether the server compressed at all.
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if enc := resp.Header.Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip; the middleware is not wired into New", enc)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("the served body is not a gzip stream: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Error("the served body does not decompress to what the handler wrote")
	}
}

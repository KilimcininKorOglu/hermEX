package serve

import (
	"errors"
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
	items := make([]string, entries)
	for i := range entries {
		items[i] = `{"id":` + strconv.Itoa(i) + `,"name":"user` + strconv.Itoa(i) + `@hermex.test","role":"member"}`
	}
	return `{"data":[` + strings.Join(items, ",") + `]}`
}

// serveCompressed runs one request through the compression middleware and returns
// the recorder.
func serveCompressed(h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	compressMiddleware(h, nil, logging.Webmail).ServeHTTP(rec, req)
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
		writeBody(t, w, body)
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
		writeBody(t, w, body)
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
			body := jsonBody(200)
			rec := serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", ct)
				writeBody(t, w, body)
			}, gzipRequest())
			if enc := rec.Header().Get("Content-Encoding"); enc != "" {
				t.Errorf("Content-Encoding = %q, want none for %s", enc, ct)
			}
			if rec.Body.String() != body {
				t.Errorf("the %s body was altered", ct)
			}
		})
	}
}

// TestDoesNotCompressSmallJSON proves the size floor holds: below it the gzip
// framing costs more than it saves.
func TestDoesNotCompressSmallJSON(t *testing.T) {
	rec := serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeBody(t, w, `{"ok":true}`)
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
	body := jsonBody(200)
	rec := serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "br")
		writeBody(t, w, body)
	}, gzipRequest())

	if enc := rec.Header().Get("Content-Encoding"); enc != "br" {
		t.Errorf("Content-Encoding = %q, want the handler's own br", enc)
	}
	if rec.Body.String() != body {
		t.Error("the handler's own encoded body was altered")
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
			writeBody(t, w, "data: first\n\n")
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("flush: %v", err)
			}
			close(flushed)
			<-released
		}), nil, logging.Webmail).ServeHTTP(rec, gzipRequest())
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
		writeBody(t, w, `{"error":"not found"}`)
	}, gzipRequest())

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if rec.Body.String() != `{"error":"not found"}` {
		t.Errorf("body = %q, want the handler's error body", rec.Body.String())
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

// errClientGone is the write failure of a client that dropped the connection.
var errClientGone = errors.New("client went away")

// brokenWriter is a response whose every body write fails, the shape of a client
// that disconnected mid-response.
type brokenWriter struct {
	*httptest.ResponseRecorder
	writes int
}

func (b *brokenWriter) Write([]byte) (int, error) {
	b.writes++
	return 0, errClientGone
}

// TestCompressFailureIsRecorded proves a gzip stream that cannot be sent is not
// lost silently: the client receives a truncated body, so the operator's log must
// carry an http.compress.fail event naming the path and the cause.
func TestCompressFailureIsRecorded(t *testing.T) {
	sink := &recordSink{}
	bw := &brokenWriter{ResponseRecorder: httptest.NewRecorder()}
	body := jsonBody(200)
	compressMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeBodyIgnoringBreak(t, w, body)
	}), logging.New(sink), logging.Webmail).ServeHTTP(bw, gzipRequest())

	e, ok := sink.last()
	if !ok {
		t.Fatal("a failed gzip response recorded no event")
	}
	wantEq(t, e.Name, "http.compress.fail", "event name")
	wantField(t, e, "path", any("/api/v1/folders"), "path field")
	if msg, isText := e.Fields["error"].(string); !isText || !strings.Contains(msg, errClientGone.Error()) {
		t.Errorf("error field = %v, want it to carry %q", e.Fields["error"], errClientGone)
	}
}

// writeBodyIgnoringBreak writes s and accepts only errClientGone, the failure the
// test's broken writer is built to return.
func writeBodyIgnoringBreak(t *testing.T, w io.Writer, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s); err != nil && !errors.Is(err, errClientGone) {
		t.Errorf("write response body: %v", err)
	}
}

// TestWriteAfterFailedFlushReportsTheFailure proves a handler learns its stream
// broke. Flush has no error result, so the failure of the held bytes must come
// back from the next Write; otherwise a handler keeps producing a body nobody
// receives, and the writer keeps sending into a dead connection.
func TestWriteAfterFailedFlushReportsTheFailure(t *testing.T) {
	bw := &brokenWriter{ResponseRecorder: httptest.NewRecorder()}
	var after error
	compressMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeBody(t, w, `{"ok":true}`) // held back: under the size floor
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
		_, after = io.WriteString(w, `{"more":true}`)
	}), nil, logging.Webmail).ServeHTTP(bw, gzipRequest())

	if !errors.Is(after, errClientGone) {
		t.Errorf("write after a failed flush returned %v, want %v", after, errClientGone)
	}
	wantEq(t, bw.writes, 1, "writes attempted on the broken connection")
}

// TestServedDaemonCompresses proves the middleware is actually installed in the
// shared HTTP base, not merely written. Every daemon builds its server through
// New, so this is what makes the change reach webmail and the administration API.
func TestServedDaemonCompresses(t *testing.T) {
	body := jsonBody(200)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeBody(t, w, body)
	})
	hs, err := New("127.0.0.1:0", h, &config.Config{}, nil, logging.Webmail, nil)
	if err != nil {
		t.Fatal(err)
	}
	runServer(t, hs)

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
	defer closeBody(t, resp)

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

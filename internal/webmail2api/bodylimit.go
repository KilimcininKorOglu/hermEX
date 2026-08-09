package webmail2api

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"
)

// defaultMaxRequestBody caps any request body the API accepts; it is the fallback
// when no operator limit has been set. It matches maxImportBytes, the largest
// legitimate body on this path (a base64-inflated .eml import), so the shared cap
// never rejects what a purpose-built endpoint already allows.
const defaultMaxRequestBody = maxImportBytes

// reqBodyLimit holds the operator-set request-body cap (bytes; 0 = use the default),
// set by SetMaxRequestBody and read live by boundBody, so the webmail daemon's poll
// can apply an edit without a restart. The API server is a per-process singleton, so
// a package-level value is the right scope (mirrors the EWS transport).
var reqBodyLimit atomic.Int64

// SetMaxRequestBody sets the maximum accepted request body in bytes (0 restores the
// built-in default). It is safe to call concurrently with request handling, so an
// operator's edit applies without a restart.
func SetMaxRequestBody(n int64) {
	if n < 0 {
		n = 0
	}
	reqBodyLimit.Store(n)
}

// maxRequestBody is the cap in force right now.
func maxRequestBody() int64 {
	if n := reqBodyLimit.Load(); n > 0 {
		return n
	}
	return defaultMaxRequestBody
}

// boundBody caps every request body at the routing chokepoint rather than in each
// handler. The API decodes bodies in dozens of places (JSON compose bodies carrying
// base64 attachments, certificate uploads, contact photos), and one unbounded read is
// enough for a single authenticated caller to exhaust the process that serves every
// mailbox on the instance.
//
// It also answers the overflow itself. A body past the cap fails its read, and every
// handler's decode path treats a read failure like malformed JSON and answers 400,
// which tells a client that its upload was broken when it was merely too big. The
// cap is installed here, so the true cause is known here: an overflowed read turns
// the handler's error response into 413 with a body that says so. Doing it at this
// one point covers every decode site, including the ones written later.
func boundBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		state := &overflowState{}
		r.Body = boundedBody(w, r.Body, maxRequestBody(), state)
		next.ServeHTTP(&overflowWriter{ResponseWriter: w, state: state}, r)
	})
}

// overflowState records whether a request body outgrew its cap. The reader and the
// response writer are separate objects, so the fact has to be carried between them.
type overflowState struct{ over bool }

// boundedBody caps a request body at n bytes and records an overflow in state, so a
// handler that installs a tighter cap of its own (the .eml import) still reports the
// right status. The observer sits outside MaxBytesReader, so it sees the error the
// reader returns.
func boundedBody(w http.ResponseWriter, body io.ReadCloser, n int64, state *overflowState) io.ReadCloser {
	return &observedBody{ReadCloser: http.MaxBytesReader(w, body, n), state: state}
}

// bodyState returns the overflow state attached to this request's body, or nil when
// the body was never bounded (no body at all).
func bodyState(r *http.Request) *overflowState {
	if ob, ok := r.Body.(*observedBody); ok {
		return ob.state
	}
	return nil
}

// observedBody flags the read that hits the cap.
type observedBody struct {
	io.ReadCloser
	state *overflowState
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && errors.As(err, new(*http.MaxBytesError)) {
		b.state.over = true
	}
	return n, err
}

// overflowWriter rewrites the handler's error response when the body overflowed:
// the status becomes 413 and the body says why, instead of a 400 that reads as "your
// request was malformed". A successful response is impossible in this state (the
// handler could not have read the body), so only an error status is rewritten.
type overflowWriter struct {
	http.ResponseWriter
	state    *overflowState
	rewrote  bool
	answered bool
}

func (w *overflowWriter) WriteHeader(status int) {
	if w.answered {
		return
	}
	w.answered = true
	if w.state.over && status >= 400 {
		w.rewrote = true
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Del("Content-Length")
		w.ResponseWriter.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.ResponseWriter.Write([]byte(`{"error":"request body too large"}`))
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *overflowWriter) Write(p []byte) (int, error) {
	if w.rewrote {
		return len(p), nil // the replacement body is already written
	}
	if !w.answered {
		w.WriteHeader(http.StatusOK)
		if w.rewrote {
			return len(p), nil
		}
	}
	return w.ResponseWriter.Write(p)
}

// Flush keeps the streaming endpoints (the event stream) working through the
// wrapper; without it their ResponseWriter would lose http.Flusher.
func (w *overflowWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

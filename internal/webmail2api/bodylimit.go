package webmail2api

import (
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
// mailbox on the instance. A request past the cap fails its read, so the handler's own
// error path answers it; the point here is that the bytes are never accumulated.
func boundBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody())
		}
		next.ServeHTTP(w, r)
	})
}

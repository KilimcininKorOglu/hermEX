package admin

import "net/http"

// maxRequestBody caps any request body the admin surface accepts. The listener
// this daemon shares with the other HTTP services leaves ReadTimeout unset (long
// polls elsewhere need that), so nothing below the application bounds a body:
// without this cap a client can stream bytes into json.Decode for as long as it
// likes. The panel's largest legitimate body is a pasted certificate and key pair,
// a few kilobytes, so a megabyte is far above real use and far below what costs
// the process anything.
//
// The cap is fixed rather than operator-tunable on purpose: it protects the
// surface an operator would tune it from, and the JSON and form bodies it covers
// have no legitimate reason to grow.
const maxRequestBody = 1 << 20

// boundBody caps every request body at the routing chokepoint rather than in each
// handler. The admin API decodes bodies in more than twenty places, and POST
// /admin/login is reachable with no authentication at all, so a per-handler guard
// would be one omission away from leaving the whole instance's management daemon
// open to memory exhaustion. A request past the cap fails its read and the
// handler's own error path answers it; the point is that the bytes are never
// accumulated.
func boundBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

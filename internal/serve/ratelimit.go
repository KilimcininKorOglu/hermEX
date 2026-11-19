package serve

import (
	"net/http"
	"strconv"
	"time"

	"hermex/internal/clientnet"
	"hermex/internal/httplimit"
)

// clientKey is the client the limiter counts a request against (clientnet.Key):
// the port removed, because a client opens a fresh source port per connection, and
// an IPv6 address cut to its /64, because a host may pick any address there. Keyed
// any finer, every connection or address would get its own budget, which is no
// limit at all.
func clientKey(r *http.Request) string {
	return clientnet.Key(ClientAddr(r))
}

// stripForwardedFor deletes any X-Forwarded-For the client supplied, so nothing
// downstream mistakes an attacker-chosen address for the real client. Only the
// outermost server (the gateway) installs it: there the header can only be a
// forgery, since no proxy of ours has run yet. The reverse proxy then writes the
// header itself from the peer address, which is what the backends read.
func stripForwardedFor(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("X-Forwarded-For")
		h.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware refuses a request once its client has passed the limiter's
// per-window budget, answering 429 with the standard retry and budget headers. A
// nil limiter returns the handler unwrapped, the same "nil disables" idiom the
// request logger uses, so a daemon that passes none behaves exactly as before.
//
// The client is keyed by clientKey: the first X-Forwarded-For hop behind the
// gateway and the peer address otherwise, so the five gateway-fronted daemons key
// on the real client rather than on the proxy. It is deliberately not keyed by the
// authenticated user: this middleware runs before any handler has verified
// credentials, so keying on a claimed identity would let anyone exhaust another
// account's budget by sending requests in its name.
func rateLimitMiddleware(h http.Handler, limiter *httplimit.Limiter) http.Handler {
	if limiter == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, retryAfter := limiter.Allow(clientKey(r))
		if allowed {
			h.ServeHTTP(w, r)
			return
		}
		secs := int(retryAfter / time.Second)
		if retryAfter%time.Second != 0 {
			secs++ // round up, so a client that waits exactly this long is admitted
		}
		hdr := w.Header()
		hdr.Set("Retry-After", strconv.Itoa(secs))
		hdr.Set("X-RateLimit-Limit", strconv.Itoa(limiter.Burst()))
		hdr.Set("X-RateLimit-Remaining", "0")
		hdr.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(retryAfter).Unix(), 10))
		http.Error(w, "too many requests", http.StatusTooManyRequests)
	})
}

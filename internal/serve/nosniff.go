package serve

import "net/http"

// nosniffMiddleware stamps X-Content-Type-Options on every response, forbidding a
// browser from second-guessing the declared Content-Type.
//
// It belongs here rather than in one handler because every daemon on this base
// returns bytes some client stored: DAV hands back the vCard and iCalendar objects
// a client PUT, EWS and ActiveSync return message content, and the gateway
// publishes all of them on the same origin as the webmail SPA. Sniffing turns any
// of those into an HTML document served from that origin.
//
// The header is set before the handler runs, so a handler that commits its status
// or writes its body first still carries it; a handler that sets the same value
// itself just overwrites an identical one.
func nosniffMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		h.ServeHTTP(w, r)
	})
}

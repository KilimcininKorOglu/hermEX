package serve

import "net/http"

// securityHeadersMiddleware stamps the response headers every daemon on this base
// needs, whatever it serves:
//
//   - X-Content-Type-Options forbids a browser from second-guessing the declared
//     Content-Type. Every daemon here returns bytes some client stored: DAV hands
//     back the vCard and iCalendar objects a client PUT, EWS and ActiveSync return
//     message content, and the gateway publishes all of them on the same origin as
//     the webmail SPA. Sniffing turns any of those into an HTML document served
//     from that origin.
//   - X-Frame-Options and the frame-ancestors policy refuse framing. The admin
//     panel is the reason they live here rather than in one handler: it serves
//     cookie-authenticated forms that create users and grant system-admin roles,
//     so a framed click on it is a privilege grant, not a nuisance.
//
// The headers are set before the handler runs, so a handler that commits its
// status or writes its body first still carries them; a handler that sets the
// same value itself just overwrites an identical one.
func securityHeadersMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		head := w.Header()
		for _, hd := range securityHeaders {
			head.Set(hd.name, hd.value)
		}
		h.ServeHTTP(w, r)
	})
}

// securityHeaders is the stamped set, named once so the gateway can strip a
// backend's own copies rather than letting both reach the client: a header that
// arrives twice is invalid, and a browser may ignore it entirely, which turns
// two layers of the same defense into none.
var securityHeaders = []struct{ name, value string }{
	{"X-Content-Type-Options", "nosniff"},
	{"X-Frame-Options", "DENY"},
	{"Content-Security-Policy", "frame-ancestors 'none'"},
}

// StripSecurityHeaders removes the stamped headers from a proxied backend's
// response, so the front door's own copy is the only one the client sees.
func StripSecurityHeaders(h http.Header) {
	for _, hd := range securityHeaders {
		h.Del(hd.name)
	}
}

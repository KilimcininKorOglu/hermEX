package gateway

import (
	"html/template"
	"net/http"
	"strings"
)

// errorPage is the body a browser gets when the front door cannot reach the
// service behind it. It is deliberately self-contained: the styling is inline
// because the very thing that is unreachable is what would have served a
// stylesheet, and it names no internal host, port or error, since the person
// reading it can act on none of that.
var errorPage = template.Must(template.New("gateway-error").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body { margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
       font: 16px/1.6 system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
       background: #f5f6f8; color: #1f2430; }
main { max-width: 32rem; padding: 2.5rem; text-align: center; }
h1 { margin: 0 0 .75rem; font-size: 1.5rem; font-weight: 600; }
p { margin: 0 0 1.25rem; color: #5a6172; }
a { display: inline-block; padding: .6rem 1.25rem; border-radius: .375rem;
    background: #2563eb; color: #fff; text-decoration: none; }
code { font-size: .8125rem; color: #8a90a0; }
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p>{{.Message}}</p>
<a href="/">Try again</a>
{{if .RequestID}}<p><code>Reference: {{.RequestID}}</code></p>{{end}}
</main>
</body>
</html>
`))

// wantsHTML reports whether the caller is a browser asking for a page. Only then
// is an HTML body appropriate: the same front door carries Outlook, ActiveSync
// devices and DAV clients, and handing one of those an HTML body where it expects
// a protocol response turns a clear transport failure into a parse failure.
func wantsHTML(r *http.Request) bool {
	for accept := range strings.SplitSeq(r.Header.Get("Accept"), ",") {
		if media, _, _ := strings.Cut(strings.TrimSpace(accept), ";"); media == "text/html" {
			return true
		}
	}
	return false
}

// writeGatewayError answers a request the front door could not forward. A browser
// gets a page explaining what happened; everything else gets the bare status it
// would have got before.
func writeGatewayError(w http.ResponseWriter, r *http.Request, status int, title, message string) {
	if !wantsHTML(r) {
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = errorPage.Execute(w, struct{ Title, Message, RequestID string }{
		Title:   title,
		Message: message,
		// The access log records this id against the failure, so a user quoting it
		// leads straight to the line that explains why.
		RequestID: w.Header().Get("X-Request-Id"),
	})
}

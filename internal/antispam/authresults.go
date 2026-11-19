package antispam

import (
	"bytes"
	"strings"

	"github.com/emersion/go-msgauth/authres"
)

// AuthResultsField returns an Authentication-Results field (RFC 8601) carrying
// this server's SPF, DKIM and DMARC results for a message, terminated by CRLF.
// authservID names this server (its mail hostname); mailFrom is the envelope
// sender SPF checked and fromDomain the From domain DMARC aligned against.
func AuthResultsField(authservID string, v Verdict, mailFrom, fromDomain string) string {
	results := []authres.Result{&authres.SPFResult{Value: resultValue(v.SPF), From: mailFrom}}
	results = append(results, dkimResults(v)...)
	results = append(results, &authres.DMARCResult{Value: resultValue(v.DMARC), From: fromDomain})
	return "Authentication-Results: " + authres.Format(authservID, results) + "\r\n"
}

// dkimResults reports one pass per verified signing domain, or the overall
// result when no signature verified.
func dkimResults(v Verdict) []authres.Result {
	if len(v.DKIMDomains) == 0 {
		return []authres.Result{&authres.DKIMResult{Value: resultValue(v.DKIM)}}
	}
	out := make([]authres.Result, 0, len(v.DKIMDomains))
	for _, d := range v.DKIMDomains {
		out = append(out, &authres.DKIMResult{Value: authres.ResultPass, Domain: d})
	}
	return out
}

// resultValue maps a check outcome to its RFC 8601 result keyword. A lookup
// error is temperror, the keyword RFC 8601 section 2.7 gives a transient failure.
func resultValue(r AuthResult) authres.ResultValue {
	if r == AuthError {
		return authres.ResultTempError
	}
	return authres.ResultValue(r)
}

// StripOwnAuthResults returns raw without the Authentication-Results fields that
// claim to come from authservID. A sender can write such a field itself, and a
// client that trusts this server's results would then read the sender's claim as
// the server's verdict; RFC 8601 section 5 requires removing them at the border.
// Fields from any other authserv-id are kept. Every other line is copied byte for
// byte. An empty authservID removes nothing.
func StripOwnAuthResults(raw []byte, authservID string) []byte {
	if authservID == "" {
		return raw
	}
	out := make([]byte, 0, len(raw))
	return appendWithoutFields(out, raw, func(line []byte) bool { return isOwnAuthResults(line, authservID) })
}

// isOwnAuthResults reports whether a header line starts an Authentication-Results
// field whose authserv-id is authservID. The authserv-id is the first token of
// the value, ending at whitespace or the ';' before the first result.
func isOwnAuthResults(line []byte, authservID string) bool {
	name, value, ok := bytes.Cut(line, []byte(":"))
	if !ok || !strings.EqualFold(strings.TrimSpace(string(name)), "Authentication-Results") {
		return false
	}
	v := strings.TrimSpace(string(value))
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	fields := strings.Fields(v)
	return len(fields) > 0 && strings.EqualFold(fields[0], authservID)
}

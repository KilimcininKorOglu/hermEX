// Package sasl holds the SASL message rules every login surface shares, so the
// IMAP, POP3 and SMTP submission servers cannot read the same message differently.
package sasl

import "strings"

// ParsePlain reads a decoded SASL PLAIN message (RFC 4616 section 2):
// [authzid] NUL authcid NUL passwd. It returns the authentication identity and
// the password, and ok=false for a malformed message or for an authorization
// identity other than the authentication identity: this server grants no proxy
// rights, and RFC 4616 has the exchange fail when the authcid may not act as the
// authzid. The comparison ignores case, as account names do.
func ParsePlain(msg string) (user, pass string, ok bool) {
	parts := strings.Split(msg, "\x00")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	if parts[0] != "" && !strings.EqualFold(parts[0], parts[1]) {
		return "", "", false
	}
	return parts[1], parts[2], true
}

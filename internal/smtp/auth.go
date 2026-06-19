package smtp

import (
	"bufio"
	"encoding/base64"
	"net/textproto"
	"strings"
)

// Authenticator is an optional Session capability. A session that implements it
// can validate SMTP AUTH credentials; the server then offers AUTH (PLAIN and
// LOGIN) — but only over a TLS link, since both mechanisms send the password as
// reversible base64. Auth reports whether the credentials are valid; the session
// records the authenticated identity for later send authorization.
type Authenticator interface {
	Auth(username, password string) bool
}

// handleAuth runs the AUTH exchange (RFC 4954) for PLAIN and LOGIN. AUTH is
// refused on a plaintext link or by a session that cannot authenticate. A
// successful exchange replies 235; a rejected credential replies 535. It reports
// whether authentication succeeded, so the caller can record it for the Received:
// trace token (RFC 3848 ESMTPA/ESMTPSA).
func (s *Server) handleAuth(w *bufio.Writer, tp *textproto.Reader, arg string, sess Session, isTLS, canAuth bool) bool {
	auth, ok := sess.(Authenticator)
	if !canAuth || !ok {
		reply(w, 503, "5.5.1 AUTH not available")
		return false
	}
	if !isTLS {
		reply(w, 538, "5.7.11 STARTTLS required before AUTH")
		return false
	}
	mechanism, initial, _ := strings.Cut(arg, " ")
	switch strings.ToUpper(mechanism) {
	case "PLAIN":
		return authPlain(w, tp, initial, auth)
	case "LOGIN":
		return authLogin(w, tp, initial, auth)
	default:
		reply(w, 504, "5.5.4 Unrecognized authentication type")
		return false
	}
}

// authPlain handles AUTH PLAIN: the credential is a single base64 token decoding
// to authzid\0authcid\0password (RFC 4616). It may arrive inline or in a
// continuation line after a 334 challenge.
func authPlain(w *bufio.Writer, tp *textproto.Reader, initial string, auth Authenticator) bool {
	resp, ok := authResponse(w, tp, initial, "")
	if !ok {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(resp)
	if err != nil {
		reply(w, 501, "5.5.2 invalid base64")
		return false
	}
	parts := strings.SplitN(string(decoded), "\x00", 3)
	if len(parts) != 3 {
		reply(w, 501, "5.5.2 malformed PLAIN response")
		return false
	}
	return finishAuth(w, auth, parts[1], parts[2])
}

// authLogin handles AUTH LOGIN: the server prompts for the base64 username then
// password (the username may arrive inline with the AUTH command).
func authLogin(w *bufio.Writer, tp *textproto.Reader, initial string, auth Authenticator) bool {
	user, ok := authResponse(w, tp, initial, "VXNlcm5hbWU6") // "Username:"
	if !ok {
		return false
	}
	username, err := base64.StdEncoding.DecodeString(user)
	if err != nil {
		reply(w, 501, "5.5.2 invalid base64")
		return false
	}
	pass, ok := authResponse(w, tp, "", "UGFzc3dvcmQ6") // "Password:"
	if !ok {
		return false
	}
	password, err := base64.StdEncoding.DecodeString(pass)
	if err != nil {
		reply(w, 501, "5.5.2 invalid base64")
		return false
	}
	return finishAuth(w, auth, string(username), string(password))
}

// authResponse returns the client's response token: the inline value when
// present, else a 334 challenge is sent and the continuation line read. A lone
// "*" aborts the exchange (RFC 4954).
func authResponse(w *bufio.Writer, tp *textproto.Reader, inline, challenge string) (string, bool) {
	resp := inline
	if resp == "" {
		reply(w, 334, challenge)
		line, err := tp.ReadLine()
		if err != nil {
			return "", false
		}
		resp = line
	}
	if resp == "*" {
		reply(w, 501, "5.7.8 authentication aborted")
		return "", false
	}
	return resp, true
}

// finishAuth validates the credentials and replies, reporting whether they were
// accepted.
func finishAuth(w *bufio.Writer, auth Authenticator, user, password string) bool {
	if !auth.Auth(user, password) {
		reply(w, 535, "5.7.8 authentication failed")
		return false
	}
	reply(w, 235, "2.7.0 authentication successful")
	return true
}

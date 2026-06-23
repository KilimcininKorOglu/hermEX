// Package webmail2api serves the webmail2 single-page app and its JSON API
// (/api/v1) backed by the hermEX directory and per-mailbox object stores.
package webmail2api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// sessionClaims is the authenticated identity carried in the webmail2 session
// cookie: the user's login and the resolved mailbox path, with an expiry. The
// cookie is HttpOnly, so the value is never read by the browser.
type sessionClaims struct {
	Email   string `json:"email"`
	Mailbox string `json:"mbox"`
	Exp     int64  `json:"exp"`
}

// mintToken signs the claims with HMAC-SHA256 and returns "payload.sig", both
// base64url-encoded.
func mintToken(secret []byte, c sessionClaims) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	b := base64.RawURLEncoding.EncodeToString(payload)
	return b + "." + sign(secret, b), nil
}

// verifyToken checks the signature and expiry and returns the claims.
func verifyToken(secret []byte, token string, now time.Time) (sessionClaims, error) {
	var c sessionClaims
	b, sig, found := strings.Cut(token, ".")
	if !found {
		return c, errors.New("malformed token")
	}
	if !hmac.Equal([]byte(sign(secret, b)), []byte(sig)) {
		return c, errors.New("bad signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, err
	}
	if now.Unix() >= c.Exp {
		return c, errors.New("token expired")
	}
	return c, nil
}

// sign returns the base64url HMAC-SHA256 of msg under secret.
func sign(secret []byte, msg string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

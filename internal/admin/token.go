// Package admin serves the hermEX administration API: it authenticates
// administrators against the directory, enforces their system/org/domain roles,
// and exposes the directory's resources over HTTP. The session is a self-signed
// HS256 JWT in a cookie — no external token library.
package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// claims is the admin session payload carried in the JWT.
type claims struct {
	Login  string `json:"sub"`
	UserID int64  `json:"uid"`
	Expiry int64  `json:"exp"`
}

var (
	errBadToken = errors.New("admin: invalid session token")
	errExpired  = errors.New("admin: session expired")
)

// joseHeader is the fixed HS256 JWT header, pre-encoded.
var joseHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// signToken mints an HS256 JWT carrying the claims, signed with secret.
func signToken(secret []byte, c claims) string {
	payload, _ := json.Marshal(c)
	signing := joseHeader + "." + base64.RawURLEncoding.EncodeToString(payload)
	return signing + "." + sign(secret, signing)
}

// verifyToken validates a token's signature (in constant time) and expiry and
// returns its claims.
func verifyToken(secret []byte, token string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, errBadToken
	}
	signing := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(parts[2]), []byte(sign(secret, signing))) {
		return claims{}, errBadToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, errBadToken
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return claims{}, errBadToken
	}
	if time.Now().Unix() >= c.Expiry {
		return claims{}, errExpired
	}
	return c, nil
}

// sign returns the base64url HMAC-SHA256 of signing under secret.
func sign(secret []byte, signing string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

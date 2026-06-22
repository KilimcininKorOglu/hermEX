// Package quarantine carries the signed, expiring credential that a quarantine-digest
// release link puts in the user's hands. The MTA's digest worker mints a token per
// quarantined message; the webmail release endpoint verifies it. Because the token is
// the SOLE credential that endpoint trusts — there is no session behind a link clicked
// from an email — it is HMAC-SHA256 signed, scoped to releasing exactly one message
// from one mailbox's Junk folder, and short-lived. The package is deliberately
// daemon-neutral (no mail-store or directory imports) so both daemons can share it.
package quarantine

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Claims is a release token's payload: which mailbox and which message it authorizes
// releasing, and when it stops being valid. It grants nothing beyond moving that one
// message out of that mailbox's Junk folder.
type Claims struct {
	Mailbox string `json:"m"` // the mailbox owner's address; the endpoint resolves it to a maildir
	UID     uint32 `json:"u"` // the IMAP UID of the quarantined message in the Junk folder
	Expiry  int64  `json:"e"` // Unix seconds after which the token is rejected
}

var (
	// ErrNoSecret reports that no signing secret is configured, so tokens can be
	// neither minted nor verified — the digest feature is effectively off.
	ErrNoSecret = errors.New("quarantine: no signing secret configured")
	// ErrBadToken reports a malformed token or a signature that does not match.
	ErrBadToken = errors.New("quarantine: invalid release token")
	// ErrExpired reports a structurally valid token whose expiry has passed.
	ErrExpired = errors.New("quarantine: release token expired")
)

// Mint signs claims into a release token of the form base64url(payload).base64url(mac).
// It returns ErrNoSecret when no secret is configured, so a caller never emits an
// unsigned link.
func Mint(secret []byte, c Claims) (string, error) {
	if len(secret) == 0 {
		return "", ErrNoSecret
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding.EncodeToString(payload)
	return enc + "." + mac(secret, enc), nil
}

// Verify checks a token's signature in constant time and its expiry against now, and
// returns its claims. It rejects a token that is malformed, signed with a different
// secret, tampered with, or expired.
func Verify(secret []byte, token string, now time.Time) (Claims, error) {
	if len(secret) == 0 {
		return Claims{}, ErrNoSecret
	}
	enc, sig, ok := strings.Cut(token, ".")
	if !ok {
		return Claims{}, ErrBadToken
	}
	if !hmac.Equal([]byte(sig), []byte(mac(secret, enc))) {
		return Claims{}, ErrBadToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return Claims{}, ErrBadToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, ErrBadToken
	}
	if !now.Before(time.Unix(c.Expiry, 0)) {
		return Claims{}, ErrExpired
	}
	return c, nil
}

// mac returns the base64url HMAC-SHA256 of signing under secret.
func mac(secret []byte, signing string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(signing))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

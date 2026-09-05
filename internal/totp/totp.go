// Package totp implements the time-based one-time password of RFC 6238 over the
// HMAC one-time password of RFC 4226, in the shape every authenticator app
// speaks: a base32 shared secret, HMAC-SHA1, six digits and a thirty second
// step.
//
// The parameters are fixed rather than configurable on purpose. An authenticator
// app reads them from the otpauth:// URI, but several popular ones ignore every
// field except the secret, so a server that offered SHA-256 or eight digits
// would hand some users a code their app can never produce.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 4226 fixes HMAC-SHA1; an authenticator app computes no other
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Step is the time step every authenticator app assumes.
	Step = 30 * time.Second
	// Digits is the code length every authenticator app assumes.
	Digits = 6
	// secretBytes is the shared secret's length. RFC 4226 requires at least 128
	// bits and recommends 160, which is also the HMAC-SHA1 output width.
	secretBytes = 20
)

// ErrInvalidSecret reports a secret that is not the base32 the enrollment
// produced. It is returned rather than a bare false so a stored secret that has
// been corrupted is distinguishable from a wrong code.
var ErrInvalidSecret = errors.New("totp: invalid secret")

// encoding is RFC 4648 base32 without padding, which is what an authenticator
// app expects in an otpauth:// URI and what a user types when scanning fails.
var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a fresh base32-encoded shared secret.
func NewSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return encoding.EncodeToString(b), nil
}

// decodeSecret accepts the secret in the forms a user may hand back: lowercase,
// and with the spaces some apps display it in.
func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	b, err := encoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil, ErrInvalidSecret
	}
	return b, nil
}

// StepAt returns the RFC 6238 counter for an instant. The caller stores the
// accepted step so the same code cannot be replayed inside its own window.
func StepAt(t time.Time) int64 {
	return t.Unix() / int64(Step/time.Second)
}

// codeAtStep computes the code for one counter value. RFC 4226 hashes an
// UNSIGNED counter, so a step before the Unix epoch has no code at all rather
// than one taken from a counter that wrapped to near 2^64; it returns the empty
// string, which no submitted code can equal.
func codeAtStep(key []byte, step int64) string {
	if step < 0 {
		return ""
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(buf[:])
	sum := m.Sum(nil)
	// RFC 4226 dynamic truncation: the low nibble of the last byte picks the
	// offset of the four bytes that carry the code.
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", Digits, v%1_000_000)
}

// Code returns the code a correct authenticator app shows at t.
func Code(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return codeAtStep(key, StepAt(t)), nil
}

// Verify checks a submitted code against the steps within skew of t and returns
// the step that matched.
//
// The caller MUST refuse a step it has already accepted for this user, because
// a code stays valid for the whole window and skew widens that window further:
// without the check, anyone who observes one code can replay it. Verify cannot
// enforce that itself, since it holds no state, so it returns the step for the
// caller to store.
//
// The comparison is constant-time, and every candidate step is compared even
// after a match, so the answer's timing does not say which step it was.
func Verify(secret, code string, t time.Time, skew int) (int64, bool) {
	key, err := decodeSecret(secret)
	if err != nil {
		return 0, false
	}
	code = strings.TrimSpace(code)
	if len(code) != Digits {
		return 0, false
	}
	if skew < 0 {
		skew = 0
	}
	now := StepAt(t)
	matched := int64(0)
	found := 0
	for d := -skew; d <= skew; d++ {
		step := now + int64(d)
		eq := subtle.ConstantTimeCompare([]byte(codeAtStep(key, step)), []byte(code))
		found |= eq
		matched |= int64(eq) * step
	}
	return matched, found == 1
}

// ProvisioningURI returns the otpauth:// URI an authenticator app reads from a
// QR code. issuer names the deployment and account names the mailbox; both are
// shown in the app's list, so both are also carried in the label, which is the
// only place the older apps read the issuer from.
func ProvisioningURI(secret, account, issuer string) string {
	label := account
	if issuer != "" {
		label = issuer + ":" + account
	}
	q := url.Values{}
	q.Set("secret", secret)
	if issuer != "" {
		q.Set("issuer", issuer)
	}
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(int(Step/time.Second)))
	// The label is a path segment, so it is escaped as one; url.Values escapes a
	// space as "+", which an authenticator app reads literally, so the query is
	// built the same way a path is.
	return "otpauth://totp/" + url.PathEscape(label) + "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
}

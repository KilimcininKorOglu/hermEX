package totp

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"strings"
)

// RecoveryCodeCount is how many single-use codes an enrollment mints. The user
// keeps them for the day the authenticator is gone, so there must be enough to
// survive a few being lost and few enough to be worth writing down.
const RecoveryCodeCount = 10

// recoveryBytes is one code's entropy. Eighty bits is far past guessing, which
// is why the stored form is a plain hash rather than a password hash: there is
// no dictionary to run against it.
const recoveryBytes = 10

// recoveryEncoding drops the padding and reads back case-insensitively, so a
// user typing a code they wrote down is not defeated by capitalisation.
var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewRecoveryCodes returns fresh codes in the form shown to the user once, and
// the hashes to store. The plaintext is never stored, so a directory read does
// not yield a way past the second factor.
func NewRecoveryCodes() (codes []string, hashes []string, err error) {
	codes = make([]string, 0, RecoveryCodeCount)
	hashes = make([]string, 0, RecoveryCodeCount)
	for range RecoveryCodeCount {
		b := make([]byte, recoveryBytes)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		c := recoveryEncoding.EncodeToString(b)
		codes = append(codes, c)
		hashes = append(hashes, HashRecoveryCode(c))
	}
	return codes, hashes, nil
}

// HashRecoveryCode returns the stored form of a code. Normalisation happens
// here and nowhere else, so a code hashes the same at mint time and at use.
func HashRecoveryCode(code string) string {
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:])
}

// MatchRecoveryCode reports which stored hash the submitted code matches, or
// -1. Every hash is compared even after a match, so the answer's timing does
// not say which code was used.
func MatchRecoveryCode(hashes []string, code string) int {
	if strings.TrimSpace(code) == "" {
		return -1
	}
	want := HashRecoveryCode(code)
	found := -1
	for i, h := range hashes {
		if subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1 {
			found = i
		}
	}
	return found
}

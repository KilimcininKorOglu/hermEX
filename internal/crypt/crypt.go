// Package crypt implements the two crypt(3) password schemes the directory
// stores: sha512-crypt ($6$), which every password hermEX sets is written in,
// and md5-crypt ($1$), accepted so a password set by an external crypt(3) tool
// interoperates. Both are fully specified and built on stdlib digests; nothing
// here is a new construction.
//
//   - sha512-crypt: Ulrich Drepper, "Unix crypt using SHA-256 and SHA-512"
//     (the SHA-crypt specification), which defines the digest sequence, the
//     rounds parameter and the permuted hash64 output encoding.
//   - md5-crypt: Poul-Henning Kamp's FreeBSD $1$ scheme, 1000 fixed rounds.
//
// A stored hash is self-describing: "$6$rounds=N$salt$digest" (the rounds
// section is omitted at the scheme default) and "$1$salt$digest". Verifying
// re-derives the hash from the stored salt and compares in constant time.
package crypt

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"strconv"
	"strings"
)

// Scheme prefixes, as they appear at the head of a stored hash.
const (
	SHA512Prefix = "$6$"
	MD5Prefix    = "$1$"
)

// sha512-crypt parameters, all from the specification. The salt is at most 16
// characters and the rounds count is clamped into [RoundsMin, RoundsMax]; a hash
// made at exactly RoundsDefault carries no rounds section.
const (
	SHA512RoundsDefault = 5000
	SHA512RoundsMin     = 1000
	SHA512RoundsMax     = 999999999
	sha512SaltLenMax    = 16
	md5SaltLenMax       = 8
)

// ErrBadHash reports a stored value that is not a hash this package understands.
var ErrBadHash = errors.New("crypt: unrecognized password hash")

// Verify reports whether password produced the stored hash. An empty hash, or
// one in a scheme this package does not implement, never matches.
func Verify(password, stored string) bool {
	var computed string
	switch {
	case strings.HasPrefix(stored, SHA512Prefix):
		salt, rounds, explicit, ok := parseSHA512(stored)
		if !ok {
			return false
		}
		computed = formatSHA512(salt, rounds, explicit, sha512Crypt([]byte(password), []byte(salt), rounds))
	case strings.HasPrefix(stored, MD5Prefix):
		salt, ok := parseMD5(stored)
		if !ok {
			return false
		}
		computed = MD5Prefix + salt + "$" + string(md5Crypt([]byte(password), []byte(salt)))
	default:
		return false
	}
	return subtle.ConstantTimeCompare([]byte(computed), []byte(stored)) == 1
}

// Rounds reports the work factor a stored sha512-crypt hash was made with, so a
// caller can decide whether it is still strong enough. A $6$ hash with no rounds
// section was made at the scheme default; any other scheme has no comparable
// factor and reports none.
func Rounds(stored string) (int, bool) {
	if !strings.HasPrefix(stored, SHA512Prefix) {
		return 0, false
	}
	_, rounds, _, ok := parseSHA512(stored)
	if !ok {
		return 0, false
	}
	return rounds, true
}

// randomSalt returns n characters drawn from the hash64 alphabet, the salt form
// crypt(3) hashes carry.
func randomSalt(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = hash64Alphabet[int(b)%len(hash64Alphabet)]
	}
	return string(buf), nil
}

// clampRounds holds a requested work factor inside the range the scheme encodes.
func clampRounds(rounds int) int {
	switch {
	case rounds < SHA512RoundsMin:
		return SHA512RoundsMin
	case rounds > SHA512RoundsMax:
		return SHA512RoundsMax
	default:
		return rounds
	}
}

// parseSHA512 splits a stored $6$ hash into its salt and work factor. explicit
// reports whether the hash carried a rounds section, which the re-derived hash
// must reproduce exactly for the comparison to be meaningful.
func parseSHA512(stored string) (salt string, rounds int, explicit bool, ok bool) {
	rest, found := strings.CutPrefix(stored, SHA512Prefix)
	if !found {
		return "", 0, false, false
	}
	rounds = SHA512RoundsDefault
	if digits, hasRounds := strings.CutPrefix(rest, "rounds="); hasRounds {
		num, after, split := strings.Cut(digits, "$")
		if !split {
			return "", 0, false, false
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			return "", 0, false, false
		}
		rounds, explicit, rest = clampRounds(n), true, after
	}
	salt, _, _ = strings.Cut(rest, "$")
	if len(salt) > sha512SaltLenMax {
		salt = salt[:sha512SaltLenMax]
	}
	return salt, rounds, explicit, true
}

// parseMD5 splits a stored $1$ hash into its salt.
func parseMD5(stored string) (salt string, ok bool) {
	rest, found := strings.CutPrefix(stored, MD5Prefix)
	if !found {
		return "", false
	}
	salt, _, _ = strings.Cut(rest, "$")
	if len(salt) > md5SaltLenMax {
		salt = salt[:md5SaltLenMax]
	}
	return salt, true
}

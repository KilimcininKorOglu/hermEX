package crypt

import (
	"crypto/sha512"
	"strconv"
)

// sha512Order is the byte order the specification writes the final digest out
// in: 21 groups of three bytes taken from three interleaved thirds of the
// digest, then the last byte on its own. 64 bytes become 86 hash64 characters.
var sha512Order = []int{
	42, 21, 0, 1, 43, 22, 23, 2, 44, 45, 24, 3, 4, 46, 25, 26, 5, 47,
	48, 27, 6, 7, 49, 28, 29, 8, 50, 51, 30, 9, 10, 52, 31, 32, 11, 53,
	54, 33, 12, 13, 55, 34, 35, 14, 56, 57, 36, 15, 16, 58, 37, 38, 17, 59,
	60, 39, 18, 19, 61, 40, 41, 20, 62,
	63,
}

// NewSHA512 hashes password with sha512-crypt at the given work factor under a
// fresh random 16-character salt, and returns the stored form.
func NewSHA512(password string, rounds int) (string, error) {
	salt, err := randomSalt(sha512SaltLenMax)
	if err != nil {
		return "", err
	}
	rounds = clampRounds(rounds)
	digest := sha512Crypt([]byte(password), []byte(salt), rounds)
	return formatSHA512(salt, rounds, rounds != SHA512RoundsDefault, digest), nil
}

// formatSHA512 assembles the stored form. The rounds section is written only
// when the hash carries one, since the scheme's own default is expressed by its
// absence and a hash must re-serialize exactly as it was stored.
func formatSHA512(salt string, rounds int, explicit bool, digest []byte) string {
	var b []byte
	b = append(b, SHA512Prefix...)
	if explicit {
		b = append(b, "rounds="...)
		b = strconv.AppendInt(b, int64(rounds), 10)
		b = append(b, '$')
	}
	b = append(b, salt...)
	b = append(b, '$')
	b = append(b, digest...)
	return string(b)
}

// sha512Crypt runs the SHA-crypt derivation and returns the hash64-encoded
// digest (the part after the last '$' of a stored hash). The step numbering in
// the comments is the specification's own.
func sha512Crypt(password, salt []byte, rounds int) []byte {
	h := sha512.New()

	// Steps 4-8: digest B over password, salt, password.
	h.Write(password)
	h.Write(salt)
	h.Write(password)
	sumB := h.Sum(nil)

	// Steps 1-3 and 9-12: digest A over password and salt, then len(password)
	// bytes taken from B, then one block per bit of len(password): B where the
	// bit is set, the password where it is clear.
	h.Reset()
	h.Write(password)
	h.Write(salt)
	h.Write(repeatTo(sumB, len(password)))
	for i := len(password); i > 0; i >>= 1 {
		if i&1 == 1 {
			h.Write(sumB)
		} else {
			h.Write(password)
		}
	}
	sumA := h.Sum(nil)
	clear(sumB)

	// Steps 13-16: sequence P, len(password) bytes of the digest of the password
	// repeated len(password) times.
	h.Reset()
	for range len(password) {
		h.Write(password)
	}
	seqP := repeatTo(h.Sum(nil), len(password))

	// Steps 17-20: sequence S, len(salt) bytes of the digest of the salt repeated
	// 16 + A[0] times, so the salt's own work depends on the password too.
	h.Reset()
	for range 16 + int(sumA[0]) {
		h.Write(salt)
	}
	seqS := repeatTo(h.Sum(nil), len(salt))

	// Step 21: the work loop. Each round digests the previous result together
	// with the password and salt sequences in an order that varies with the round
	// number, so the whole chain has to be walked in sequence. The digest is
	// summed into a reused buffer: at 600k rounds a per-round allocation is the
	// difference between a login costing 150ms and costing noticeably more.
	buf := make([]byte, 0, sha512.Size)
	for i := range rounds {
		h.Reset()
		if i&1 == 1 {
			h.Write(seqP)
		} else {
			h.Write(sumA)
		}
		if i%3 != 0 {
			h.Write(seqS)
		}
		if i%7 != 0 {
			h.Write(seqP)
		}
		if i&1 == 1 {
			h.Write(sumA)
		} else {
			h.Write(seqP)
		}
		copy(sumA, h.Sum(buf[:0]))
	}
	clear(seqP)
	clear(seqS)

	return hash64(permute(sumA, sha512Order))
}

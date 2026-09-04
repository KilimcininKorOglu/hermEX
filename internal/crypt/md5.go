package crypt

// #nosec G501 -- the $1$ scheme is MD5 by definition; this code only verifies hashes set elsewhere and never issues one
import "crypto/md5"

// md5Rounds is the fixed work factor of the $1$ scheme. Unlike sha512-crypt it
// is not tunable and is not written into the hash.
const md5Rounds = 1000

// md5Order is the byte order the $1$ scheme writes its 16-byte digest out in:
// 5 groups of three bytes, then the last byte, giving 22 hash64 characters.
var md5Order = []int{
	12, 6, 0, 13, 7, 1, 14, 8, 2, 15, 9, 3, 5, 10, 4, 11,
}

// md5Crypt runs Poul-Henning Kamp's $1$ derivation and returns the
// hash64-encoded digest. There is deliberately no exported generator: hermEX
// never sets a password in this scheme, it only verifies hashes set elsewhere,
// and md5-crypt's 1000 fixed rounds are far below what a stored credential
// needs today (a $1$ hash is re-hashed at the current factor on next login).
func md5Crypt(password, salt []byte) []byte {
	// #nosec G401 -- the $1$ scheme is MD5 by definition; this code only verifies hashes set elsewhere and never issues one
	h := md5.New()

	// Digest B over password, salt, password.
	h.Write(password)
	h.Write(salt)
	h.Write(password)
	sumB := h.Sum(nil)

	// Digest A over password, the magic prefix, the salt, then len(password)
	// bytes of B, then one byte per bit of len(password). The trailing byte loop
	// is the scheme's known quirk (a NUL where the bit is set, the password's
	// first byte where it is clear) and is reproduced because interoperability,
	// not elegance, is what a stored hash needs.
	h.Reset()
	h.Write(password)
	h.Write([]byte(MD5Prefix))
	h.Write(salt)
	h.Write(repeatTo(sumB, len(password)))
	for i := len(password); i > 0; i >>= 1 {
		if i&1 == 1 {
			h.Write([]byte{0})
		} else {
			h.Write(password[:1])
		}
	}
	sumA := h.Sum(nil)
	clear(sumB)

	// The work loop, the same shape as sha512-crypt's but with the password
	// itself where that scheme uses a derived sequence.
	buf := make([]byte, 0, md5.Size)
	for i := range md5Rounds {
		h.Reset()
		if i&1 == 1 {
			h.Write(password)
		} else {
			h.Write(sumA)
		}
		if i%3 != 0 {
			h.Write(salt)
		}
		if i%7 != 0 {
			h.Write(password)
		}
		if i&1 == 1 {
			h.Write(sumA)
		} else {
			h.Write(password)
		}
		copy(sumA, h.Sum(buf[:0]))
	}

	return hash64(permute(sumA, md5Order))
}

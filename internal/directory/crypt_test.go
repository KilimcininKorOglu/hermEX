package directory

import (
	"testing"

	"hermex/internal/crypt"
)

// TestCryptInterop proves sqlCryptVerify accepts both sha512-crypt ($6$, the
// directory's own scheme) and md5-crypt ($1$) hashes, round-trips its own
// generated hash, accepts external vectors it did not produce, and rejects wrong
// passwords and unsupported schemes.
func TestCryptInterop(t *testing.T) {
	const pw = "Hello world!"

	sha6, err := crypt.NewSHA512(pw, crypt.SHA512RoundsDefault)
	mustNoErr(t, "generate a sha512-crypt hash", err)
	// An external sha512-crypt vector (OpenSSL's own output for this password and
	// salt), so the interop claim rests on a hash this tree did not make.
	const external = "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"
	for _, h := range []string{sha6, external} {
		wantVerify(t, "the sha512-crypt hash "+h, pw, h, true)
		wantVerify(t, "the sha512-crypt hash "+h+" with a wrong password", "wrong", h, false)
	}

	// Poul-Henning Kamp's canonical md5-crypt vector for "Hello world!", a hash
	// this code did not generate, so verifying it proves real external interop.
	wantVerify(t, "the canonical external md5-crypt vector", pw, "$1$saltstri$YMyguxXMBpd2TEZ.vS/3q1", true)

	// The directory's own generator round-trips through verify.
	h, err := sqlCryptNewHash("s3cret")
	mustNoErr(t, "generate a directory hash", err)
	wantVerify(t, "a freshly generated hash", "s3cret", h, true)
	wantVerify(t, "a freshly generated hash with a wrong password", "nope", h, false)

	// An empty hash or an unrecognized scheme never matches.
	for _, bad := range []string{"", "plaintext", "$2y$10$unsupportedbcrypthashvalue"} {
		wantVerify(t, "the empty or unsupported hash "+bad, "x", bad, false)
	}
}

// wantVerify checks whether sqlCryptVerify accepts one password against one hash.
func wantVerify(t *testing.T, what, pw, hash string, want bool) {
	t.Helper()
	wantEq(t, what+" verifies", sqlCryptVerify(pw, hash), want)
}

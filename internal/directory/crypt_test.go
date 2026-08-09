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
	if err != nil {
		t.Fatal(err)
	}
	// An external sha512-crypt vector (OpenSSL's own output for this password and
	// salt), so the interop claim rests on a hash this tree did not make.
	const external = "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"
	for _, h := range []string{sha6, external} {
		if !sqlCryptVerify(pw, h) {
			t.Errorf("hash %q failed to verify its password", h)
		}
		if sqlCryptVerify("wrong", h) {
			t.Errorf("hash %q verified a wrong password", h)
		}
	}

	// Poul-Henning Kamp's canonical md5-crypt vector for "Hello world!", a hash
	// this code did not generate, so verifying it proves real external interop.
	if !sqlCryptVerify(pw, "$1$saltstri$YMyguxXMBpd2TEZ.vS/3q1") {
		t.Error("the canonical external md5-crypt vector failed to verify")
	}

	// The directory's own generator round-trips through verify.
	h, err := sqlCryptNewHash("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !sqlCryptVerify("s3cret", h) || sqlCryptVerify("nope", h) {
		t.Error("a freshly generated hash did not verify exactly its own password")
	}

	// An empty hash or an unrecognized scheme never matches.
	for _, bad := range []string{"", "plaintext", "$2y$10$unsupportedbcrypthashvalue"} {
		if sqlCryptVerify("x", bad) {
			t.Errorf("an empty or unsupported hash matched: %q", bad)
		}
	}
}

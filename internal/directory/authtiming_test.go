package directory

import "testing"

// TestDecoyPasswordHashValid guards the login timing-oracle mitigation: the decoy
// hash authenticateRow runs for an absent/unusable account must be a well-formed
// sha512-crypt hash so the verify performs the full KDF work (the same cost as a
// real wrong-password check). If it were malformed, sqlCryptVerify would return
// early and the timing difference the mitigation exists to erase would reopen.
func TestDecoyPasswordHashValid(t *testing.T) {
	// A valid $6$ hash makes sqlCryptVerify run the KDF and return false for a
	// wrong password rather than bailing out on an unrecognized prefix.
	if !hasSHA512CryptPrefix(decoyPasswordHash) {
		t.Fatalf("decoy hash is not sha512-crypt ($6$): %q", decoyPasswordHash)
	}
	// Wrong password against the decoy must be rejected (never a silent accept).
	if sqlCryptVerify("definitely-not-the-decoy-password", decoyPasswordHash) {
		t.Fatal("decoy hash accepted a wrong password")
	}
	// The decoy hash must round-trip its own seed value, proving the KDF actually
	// ran and the constant was not corrupted.
	if !sqlCryptVerify("decoy-not-a-real-password", decoyPasswordHash) {
		t.Fatal("decoy hash failed to verify its own seed value; KDF not exercised")
	}
}

func hasSHA512CryptPrefix(h string) bool {
	return len(h) >= 3 && h[:3] == "$6$"
}

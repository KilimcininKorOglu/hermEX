package totp

import (
	"strings"
	"testing"
)

// TestRecoveryCodesAreDistinctAndMatchTheirHashes is the round trip the login
// path depends on: the code shown once has to match the hash that was stored.
func TestRecoveryCodesAreDistinctAndMatchTheirHashes(t *testing.T) {
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount || len(hashes) != RecoveryCodeCount {
		t.Fatalf("minted %d codes and %d hashes", len(codes), len(hashes))
	}
	seen := map[string]bool{}
	for i, c := range codes {
		if seen[c] {
			t.Fatalf("code %d repeats", i)
		}
		seen[c] = true
		if got := MatchRecoveryCode(hashes, c); got != i {
			t.Errorf("code %d matched index %d", i, got)
		}
	}
}

// TestRecoveryCodeIsNotStoredInTheClear is the point of hashing them: a
// directory read must not hand over a way past the second factor.
func TestRecoveryCodeIsNotStoredInTheClear(t *testing.T) {
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	for i, h := range hashes {
		if strings.Contains(strings.ToUpper(h), codes[i]) {
			t.Fatalf("hash %d carries its code", i)
		}
	}
}

// TestRecoveryCodeIsAcceptedAsTheUserWroteItDown keeps a code usable after it
// has been copied onto paper, which loses the case and gains spaces.
func TestRecoveryCodeIsAcceptedAsTheUserWroteItDown(t *testing.T) {
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	c := codes[3]
	for _, form := range []string{strings.ToLower(c), " " + c + " ", c[:4] + " " + c[4:]} {
		if got := MatchRecoveryCode(hashes, form); got != 3 {
			t.Errorf("%q matched %d, want 3", form, got)
		}
	}
}

// TestRecoveryCodeRefusesAnythingElse covers the two inputs a login path can
// receive without a user having typed a code: a wrong one, and none at all. An
// empty code must never match, because a stored hash of the empty string would
// otherwise open the account.
func TestRecoveryCodeRefusesAnythingElse(t *testing.T) {
	_, hashes, err := NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "   ", "NOTACODE", HashRecoveryCode("")} {
		if got := MatchRecoveryCode(hashes, bad); got != -1 {
			t.Errorf("%q matched index %d", bad, got)
		}
	}
	if got := MatchRecoveryCode(nil, "ANY"); got != -1 {
		t.Errorf("an empty code list matched: %d", got)
	}
}

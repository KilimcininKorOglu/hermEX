package admin

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// TestVerifyTokenPinsTheJOSEHeader proves the verifier does not take its
// algorithm from the token. A token whose header says something else is refused
// on its face, so the choice of algorithm stays the server's whatever the caller
// sends.
func TestVerifyTokenPinsTheJOSEHeader(t *testing.T) {
	secret := []byte("admin-token-header-test-secret")
	good := signToken(secret, claims{Login: "alice@hermex.test", UserID: 4, Expiry: time.Now().Add(time.Hour).Unix()})
	if _, err := verifyToken(secret, good); err != nil {
		t.Fatalf("a freshly minted token was refused: %v", err)
	}

	// Re-sign the same claims under a different header. The signature is valid for
	// what it covers, so only the header check can refuse this.
	parts := strings.Split(good, ".")
	if len(parts) != 3 {
		t.Fatalf("minted token has %d parts", len(parts))
	}
	for _, hdr := range []string{
		`{"alg":"none","typ":"JWT"}`,
		`{"alg":"HS512","typ":"JWT"}`,
		`{"typ":"JWT","alg":"HS256"}`, // same meaning, different bytes
	} {
		enc := base64.RawURLEncoding.EncodeToString([]byte(hdr))
		signing := enc + "." + parts[1]
		forged := signing + "." + sign(secret, signing)
		if _, err := verifyToken(secret, forged); err == nil {
			t.Errorf("header %s was accepted", hdr)
		}
	}
}

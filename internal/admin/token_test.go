package admin

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// TestTokenRoundTrip proves a signed session token verifies back to its claims.
func TestTokenRoundTrip(t *testing.T) {
	secret := []byte("s3cret-signing-key")
	c := claims{Login: "admin@hermex.test", UserID: 7, Expiry: time.Now().Add(time.Hour).Unix()}

	got, err := verifyToken(secret, signToken(secret, c))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != c {
		t.Errorf("round-trip = %+v, want %+v", got, c)
	}
}

// TestTokenRejectsTampering proves a token forged under the wrong secret, one
// whose payload was edited, and a malformed token are all rejected.
func TestTokenRejectsTampering(t *testing.T) {
	secret := []byte("key")
	tok := signToken(secret, claims{Login: "a", UserID: 1, Expiry: time.Now().Add(time.Hour).Unix()})

	if _, err := verifyToken([]byte("other-secret"), tok); err == nil {
		t.Error("a token verified under the wrong secret")
	}

	parts := strings.Split(tok, ".")
	forged := parts[0] + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"a","uid":999,"exp":9999999999}`)) +
		"." + parts[2]
	if _, err := verifyToken(secret, forged); err == nil {
		t.Error("a token with an edited payload verified")
	}

	for _, bad := range []string{"", "not-a-jwt", "a.b", "a.b.c.d"} {
		if _, err := verifyToken(secret, bad); err == nil {
			t.Errorf("a malformed token verified: %q", bad)
		}
	}
}

// TestTokenExpiry proves an expired token is rejected.
func TestTokenExpiry(t *testing.T) {
	secret := []byte("key")
	tok := signToken(secret, claims{Login: "a", UserID: 1, Expiry: time.Now().Add(-time.Minute).Unix()})
	if _, err := verifyToken(secret, tok); err == nil {
		t.Error("an expired token verified")
	}
}

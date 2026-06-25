package webmail2api

import (
	"encoding/base64"
	"testing"
)

// TestVapidKeysStableAndValid proves the VAPID keypair derived from the secret is
// stable (the same secret yields the same keys, so it survives restarts with no
// storage and matches across instances), well-formed (a 65-byte uncompressed P-256
// public point and a 32-byte private scalar, as the web-push library expects), and
// secret-bound (a different secret yields a different key).
func TestVapidKeysStableAndValid(t *testing.T) {
	pub1, priv1, err := vapidKeys([]byte("secret-one"))
	if err != nil {
		t.Fatal(err)
	}
	pub2, priv2, err := vapidKeys([]byte("secret-one"))
	if err != nil {
		t.Fatal(err)
	}
	if pub1 != pub2 || priv1 != priv2 {
		t.Fatal("vapidKeys is not stable for the same secret; a restart would invalidate every subscription")
	}
	pubBytes, err := base64.RawURLEncoding.DecodeString(pub1)
	if err != nil || len(pubBytes) != 65 || pubBytes[0] != 0x04 {
		t.Fatalf("public key is not a 65-byte uncompressed P-256 point: %d bytes, err %v", len(pubBytes), err)
	}
	privBytes, err := base64.RawURLEncoding.DecodeString(priv1)
	if err != nil || len(privBytes) != 32 {
		t.Fatalf("private key is not a 32-byte scalar: %d bytes, err %v", len(privBytes), err)
	}
	if pubOther, _, _ := vapidKeys([]byte("secret-two")); pubOther == pub1 {
		t.Fatal("different secrets produced the same public key")
	}
}

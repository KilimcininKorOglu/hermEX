package webmail2api

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// TestTokenRoundTrip proves a minted session token verifies back to its claims.
func TestTokenRoundTrip(t *testing.T) {
	secret := []byte("token-secret")
	now := time.Now()
	c := sessionClaims{Email: "alice@hermex.test", Mailbox: "/mb/alice", Jti: "j1", Exp: now.Add(time.Hour).Unix()}
	tok, err := mintToken(secret, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifyToken(secret, tok, now)
	if err != nil || got != c {
		t.Errorf("verify = %+v, %v; want %+v", got, err, c)
	}
}

// TestTokenRefusesAForgery proves the session cookie cannot be forged: a token
// signed under another secret, one whose payload names another account or mailbox
// with the old signature, an unsigned one, and malformed ones are all refused.
// The payload carries the mailbox path, so an accepted forgery opens any mailbox.
func TestTokenRefusesAForgery(t *testing.T) {
	secret := []byte("token-secret")
	now := time.Now()
	tok, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: "/mb/alice", Exp: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyToken([]byte("other-secret"), tok, now); err == nil {
		t.Error("a token verified under another secret")
	}
	_, sig, found := strings.Cut(tok, ".")
	if !found {
		t.Fatalf("minted token %q has no payload.signature separator", tok)
	}
	forged := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"email":"bob@hermex.test","mbox":"/mb/bob","exp":9999999999}`))
	notJSON := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	for name, bad := range map[string]string{
		"edited payload":   forged + "." + sig,
		"no signature":     forged + ".",
		"no separator":     forged,
		"empty":            "",
		"garbage payload":  notJSON + "." + sig,
		"extra segment":    tok + ".x",
		"signature as b64": forged + "." + base64.RawURLEncoding.EncodeToString([]byte("x")),
	} {
		if c, err := verifyToken(secret, bad, now); err == nil {
			t.Errorf("%s verified as %+v", name, c)
		}
	}
}

// TestTokenRefusesAnExpiredOne proves a token is refused from its expiry second on.
func TestTokenRefusesAnExpiredOne(t *testing.T) {
	secret := []byte("token-secret")
	now := time.Now()
	tok, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Exp: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyToken(secret, tok, now); err == nil {
		t.Error("a token verified at its expiry second")
	}
	if _, err := verifyToken(secret, tok, now.Add(-time.Second)); err != nil {
		t.Errorf("a token one second before expiry was refused: %v", err)
	}
}

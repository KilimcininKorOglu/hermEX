package quarantine

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	testSecret = []byte("a-32-byte-or-longer-signing-secret!!")
	testNow    = time.Unix(1_700_000_000, 0)
)

// TestTokenRoundTrip proves a freshly minted token verifies back to the same claims
// before its expiry.
func TestTokenRoundTrip(t *testing.T) {
	want := Claims{Mailbox: "alice@hermex.test", UID: 42, Expiry: testNow.Add(time.Hour).Unix()}
	tok, err := Mint(testSecret, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(testSecret, tok, testNow)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != want {
		t.Errorf("claims = %+v, want %+v", got, want)
	}
}

// mustMint mints a token under testSecret and fails the test when minting fails;
// otherwise an empty token would be refused by Verify and every rejection test
// below would pass without exercising a real signature.
func mustMint(t *testing.T, c Claims) string {
	t.Helper()
	tok, err := Mint(testSecret, c)
	if err != nil || tok == "" {
		t.Fatalf("Mint = %q, %v", tok, err)
	}
	return tok
}

// splitToken cuts a token into its payload and signature and fails the test
// when the separator is missing, so a forgery is built from real parts.
func splitToken(t *testing.T, tok string) (payload, sig string) {
	t.Helper()
	payload, sig, found := strings.Cut(tok, ".")
	if !found || payload == "" || sig == "" {
		t.Fatalf("token %q is not payload.signature", tok)
	}
	return payload, sig
}

// TestTokenRejectsTamperedSignature proves flipping the signature fails verification,
// the token cannot be forged without the secret.
func TestTokenRejectsTamperedSignature(t *testing.T) {
	tok := mustMint(t, Claims{Mailbox: "alice@hermex.test", UID: 1, Expiry: testNow.Add(time.Hour).Unix()})
	enc, sig := splitToken(t, tok)
	if sig == "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatal("the minted signature equals the forgery, so the test proves nothing")
	}
	forged := enc + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := Verify(testSecret, forged, testNow); !errors.Is(err, ErrBadToken) {
		t.Errorf("tampered signature err = %v, want ErrBadToken", err)
	}
}

// TestTokenRejectsTamperedPayload proves editing the payload (e.g. to point at another
// mailbox or message) invalidates the signature.
func TestTokenRejectsTamperedPayload(t *testing.T) {
	tok := mustMint(t, Claims{Mailbox: "alice@hermex.test", UID: 1, Expiry: testNow.Add(time.Hour).Unix()})
	// Re-sign attempt with a different payload but the original signature.
	_, sig := splitToken(t, tok)
	other := mustMint(t, Claims{Mailbox: "victim@hermex.test", UID: 99, Expiry: testNow.Add(time.Hour).Unix()})
	otherEnc, _ := splitToken(t, other)
	forged := otherEnc + "." + sig
	if _, err := Verify(testSecret, forged, testNow); !errors.Is(err, ErrBadToken) {
		t.Errorf("tampered payload err = %v, want ErrBadToken", err)
	}
}

// TestTokenRejectsWrongSecret proves a token signed with one secret does not verify
// under another, so a leak of one daemon's secret cannot mint links for another.
func TestTokenRejectsWrongSecret(t *testing.T) {
	tok := mustMint(t, Claims{Mailbox: "alice@hermex.test", UID: 1, Expiry: testNow.Add(time.Hour).Unix()})
	if _, err := Verify([]byte("a-different-secret-entirely-32bytes!"), tok, testNow); !errors.Is(err, ErrBadToken) {
		t.Errorf("wrong-secret err = %v, want ErrBadToken", err)
	}
}

// TestTokenRejectsExpired proves a token is refused once its expiry has passed, so a
// stale digest link cannot be used indefinitely.
func TestTokenRejectsExpired(t *testing.T) {
	tok := mustMint(t, Claims{Mailbox: "alice@hermex.test", UID: 1, Expiry: testNow.Unix()})
	if _, err := Verify(testSecret, tok, testNow); !errors.Is(err, ErrExpired) {
		t.Errorf("at-expiry err = %v, want ErrExpired", err)
	}
	if _, err := Verify(testSecret, tok, testNow.Add(time.Second)); !errors.Is(err, ErrExpired) {
		t.Errorf("past-expiry err = %v, want ErrExpired", err)
	}
}

// TestTokenNoSecret proves that without a configured secret neither minting nor
// verifying is possible, the feature is off rather than emitting unsigned links.
func TestTokenNoSecret(t *testing.T) {
	if _, err := Mint(nil, Claims{Mailbox: "a@b", UID: 1, Expiry: testNow.Unix()}); !errors.Is(err, ErrNoSecret) {
		t.Errorf("mint without secret err = %v, want ErrNoSecret", err)
	}
	if _, err := Verify(nil, "x.y", testNow); !errors.Is(err, ErrNoSecret) {
		t.Errorf("verify without secret err = %v, want ErrNoSecret", err)
	}
}

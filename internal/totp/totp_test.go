package totp

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// rfcSecret is the shared secret of the RFC 6238 test vectors, "12345678901234567890"
// in ASCII, in the base32 an authenticator app is given.
var rfcSecret = base32.StdEncoding.WithPadding(base32.NoPadding).
	EncodeToString([]byte("12345678901234567890"))

// TestCodeMatchesTheRFCVectors is the interop guarantee: an authenticator app
// computes RFC 6238, so a code this package produces has to equal the published
// answer for the same secret and instant, or no app will ever agree with it.
// The RFC prints eight digits; a six-digit code is the low six of the same
// truncation.
func TestCodeMatchesTheRFCVectors(t *testing.T) {
	for _, c := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	} {
		got, err := Code(rfcSecret, time.Unix(c.unix, 0))
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("Code at %d = %q, want %q", c.unix, got, c.want)
		}
	}
}

// TestVerifyAcceptsTheNeighbouringSteps keeps a user whose phone clock is a few
// seconds off from being locked out, which is the whole reason skew exists.
func TestVerifyAcceptsTheNeighbouringSteps(t *testing.T) {
	now := time.Unix(1111111111, 0)
	for _, d := range []time.Duration{-Step, 0, Step} {
		code, err := Code(rfcSecret, now.Add(d))
		if err != nil {
			t.Fatal(err)
		}
		step, ok := Verify(rfcSecret, code, now, 1)
		if !ok {
			t.Errorf("the code from %v was refused", d)
			continue
		}
		if want := StepAt(now.Add(d)); step != want {
			t.Errorf("the code from %v matched step %d, want %d", d, step, want)
		}
	}
}

// TestVerifyRefusesAStepOutsideTheWindow is the other half: the window must not
// be wider than asked for, or a code stays usable long after it was shown.
func TestVerifyRefusesAStepOutsideTheWindow(t *testing.T) {
	now := time.Unix(1111111111, 0)
	code, err := Code(rfcSecret, now.Add(2*Step))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Verify(rfcSecret, code, now, 1); ok {
		t.Error("a code two steps away was accepted")
	}
	if _, ok := Verify(rfcSecret, "000000", now, 1); ok {
		// The all-zero code is a real code for some secret and instant, so this
		// asserts it is not one here rather than that it is malformed.
		t.Error("a wrong code was accepted")
	}
	if _, ok := Verify(rfcSecret, "12345", now, 1); ok {
		t.Error("a short code was accepted")
	}
}

// TestATimeBeforeTheEpochHasNoCode covers the one input that would otherwise
// hash a counter near 2^64 instead of the negative step it came from, which
// would make two different instants share a code.
func TestATimeBeforeTheEpochHasNoCode(t *testing.T) {
	before := time.Unix(-1000, 0)
	code, err := Code(rfcSecret, before)
	if err != nil {
		t.Fatal(err)
	}
	if code != "" {
		t.Errorf("a pre-epoch instant produced the code %q", code)
	}
	if _, ok := Verify(rfcSecret, "000000", before, 1); ok {
		t.Error("a code verified against a pre-epoch instant")
	}
}

// TestVerifyReportsTheStepSoAReplayCanBeRefused pins the contract the caller
// relies on. A code stays valid for its whole step, so without the returned step
// the caller cannot tell a second use from a first, and anyone who observes one
// code can replay it inside the window.
func TestVerifyReportsTheStepSoAReplayCanBeRefused(t *testing.T) {
	now := time.Unix(1111111111, 0)
	code, err := Code(rfcSecret, now)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := Verify(rfcSecret, code, now, 1)
	if !ok {
		t.Fatal("the code was refused")
	}
	// Half a step later the same code still verifies, and reports the SAME step,
	// which is what lets the caller refuse it.
	second, ok := Verify(rfcSecret, code, now.Add(Step/2), 1)
	if !ok {
		t.Fatal("the code was refused inside its own step")
	}
	if first != second {
		t.Errorf("the same code reported steps %d and %d", first, second)
	}
}

// TestSecretIsAcceptedInTheFormsAUserTypesIt keeps manual entry working, since a
// user who cannot scan the QR code types the secret out of the app's own
// display, which groups it in spaces and may lowercase it.
func TestSecretIsAcceptedInTheFormsAUserTypesIt(t *testing.T) {
	now := time.Unix(1111111111, 0)
	want, err := Code(rfcSecret, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		strings.ToLower(rfcSecret),
		rfcSecret[:4] + " " + rfcSecret[4:],
		"  " + rfcSecret + "  ",
	} {
		got, err := Code(s, now)
		if err != nil {
			t.Errorf("Code(%q) = %v", s, err)
			continue
		}
		if got != want {
			t.Errorf("Code(%q) = %q, want %q", s, got, want)
		}
	}
	if _, err := Code("not base32 !!", now); err == nil {
		t.Error("a malformed secret was accepted")
	}
	if _, err := Code("", now); err == nil {
		t.Error("an empty secret was accepted")
	}
}

// TestNewSecretIsRandomAndUsable proves the enrollment mints something the rest
// of the package can actually use.
func TestNewSecretIsRandomAndUsable(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two enrollments produced the same secret")
	}
	now := time.Now()
	code, err := Code(a, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Verify(a, code, now, 1); !ok {
		t.Error("a freshly minted secret did not verify its own code")
	}
	if _, ok := Verify(b, code, now, 1); ok {
		t.Error("a code verified against another secret")
	}
}

// TestProvisioningURIEscapesTheLabel is what an authenticator app parses. A
// space encoded as "+" is read literally by several apps, so the account would
// show with a plus in it, and an unescaped colon would split the label.
func TestProvisioningURIEscapesTheLabel(t *testing.T) {
	uri := ProvisioningURI("ABCD", "alice@hermex.test", "hermEX Mail")
	if !strings.HasPrefix(uri, "otpauth://totp/hermEX%20Mail:alice@hermex.test?") {
		t.Errorf("label not escaped as a path segment: %s", uri)
	}
	if strings.Contains(uri, "+") {
		t.Errorf("a space was encoded as + : %s", uri)
	}
	for _, want := range []string{"secret=ABCD", "issuer=hermEX%20Mail", "digits=6", "period=30", "algorithm=SHA1"} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI is missing %s: %s", want, uri)
		}
	}
}

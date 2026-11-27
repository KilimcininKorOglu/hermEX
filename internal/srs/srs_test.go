package srs

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	secret = []byte("test secret")
	now    = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
)

// mustForward rewrites addr at forwarder and fails the test when the rewrite
// errs, so no case checks an empty result by accident.
func mustForward(t *testing.T, addr, forwarder string, at time.Time) string {
	t.Helper()
	out, err := Forward(addr, forwarder, secret, at)
	if err != nil {
		t.Fatalf("Forward(%q): %v", addr, err)
	}
	return out
}

// TestForwardThenReverseRestoresTheSender: a rewritten sender is an address at
// the forwarder, in the SRS0 form, and a bounce to it returns to the original
// sender, even when the bounce comes back in another case.
func TestForwardThenReverseRestoresTheSender(t *testing.T) {
	out := mustForward(t, "Alice.B@origin.example", "forward.example", now)
	if !strings.HasPrefix(out, "SRS0=") || !strings.HasSuffix(out, "=origin.example=Alice.B@forward.example") {
		t.Fatalf("Forward = %q", out)
	}
	for _, back := range []string{out, strings.ToLower(out), strings.ToUpper(out)} {
		got, err := Reverse(back, secret, now.Add(20*24*time.Hour))
		if err != nil || !strings.EqualFold(got, "Alice.B@origin.example") {
			t.Errorf("Reverse(%q) = %q, %v", back, got, err)
		}
	}
}

// TestReverseRefusesWhatItDidNotMint covers the addresses a sender can make up
// to have this server relay a "bounce" to a target of their choice: a guessed or
// altered hash, another target under a valid hash, a different secret, an
// expired or future day, and a malformed address.
func TestReverseRefusesWhatItDidNotMint(t *testing.T) {
	good := mustForward(t, "alice@origin.example", "forward.example", now)
	h := strings.Split(good, "=")[1]
	cases := map[string]error{
		strings.Replace(good, h, "AAAAAAAA", 1):                        ErrBadHash,
		strings.Replace(good, "=alice@", "=victim@", 1):                ErrBadHash,
		strings.Replace(good, "=origin.example=", "=evil.example=", 1): ErrBadHash,
		"SRS0=" + h + "=AA=origin.example":                             ErrNotSRS,
		"alice@forward.example":                                        ErrNotSRS,
		"SRS0==@forward.example":                                       ErrNotSRS,
	}
	for addr, want := range cases {
		if _, err := Reverse(addr, secret, now); !errors.Is(err, want) {
			t.Errorf("Reverse(%q) error = %v, want %v", addr, err, want)
		}
	}
	if _, err := Reverse(good, []byte("other secret"), now); !errors.Is(err, ErrBadHash) {
		t.Errorf("another secret verified the address: %v", err)
	}
	if _, err := Reverse(good, secret, now.Add(MaxAge+24*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Errorf("an address older than MaxAge verified: %v", err)
	}
	if _, err := Reverse(good, secret, now.Add(-48*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Errorf("an address dated in the future verified: %v", err)
	}
}

// TestForwardLeavesWhatItCannotRewrite: the null sender stays empty (a bounce is
// never rewritten), and a sender whose rewrite would pass the 64-octet
// local-part limit of RFC 5321 section 4.5.3.1.1 is left as it was.
func TestForwardLeavesWhatItCannotRewrite(t *testing.T) {
	if got := mustForward(t, "", "forward.example", now); got != "" {
		t.Errorf("the null sender became %q", got)
	}
	long := strings.Repeat("a", 40) + "@origin.example"
	if got := mustForward(t, long, "forward.example", now); got != long {
		t.Errorf("an over-long rewrite was returned: %q", got)
	}
	if got := mustForward(t, "noat", "forward.example", now); got != "noat" {
		t.Errorf("an address without a domain became %q", got)
	}
}

package authlimit

import (
	"testing"
	"time"
)

// TestLockoutAfterThreshold proves the limiter admits an account until the failure
// threshold, then refuses it for the lockout window, then admits it again once the
// cooldown elapses, the full brute-force-blunting cycle on an injected clock.
func TestLockoutAfterThreshold(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(3, time.Minute, 5*time.Minute)
	l.now = func() time.Time { return now }

	const acct = "victim@hermex.test"
	for i := range 2 {
		if !l.Allowed("", acct) {
			t.Fatalf("attempt %d refused early", i)
		}
		l.Fail("", acct)
	}
	// Third failure trips the lockout.
	if !l.Allowed("", acct) {
		t.Fatal("third attempt refused before threshold")
	}
	l.Fail("", acct)
	if l.Allowed("", acct) {
		t.Fatal("the account was not locked out after reaching the threshold")
	}
	// Still locked one second before the cooldown ends.
	now = now.Add(5*time.Minute - time.Second)
	if l.Allowed("", acct) {
		t.Fatal("the account was admitted before the lockout elapsed")
	}
	// Admitted once the cooldown passes.
	now = now.Add(2 * time.Second)
	if !l.Allowed("", acct) {
		t.Fatal("the account is still locked after the cooldown elapsed")
	}
}

// TestSucceedClears proves a success wipes accrued failures so a user who finally
// types the right password is not held to a stale counter, and an attempt with
// neither axis (an unkeyable caller) is never tracked.
func TestSucceedClears(t *testing.T) {
	l := New(3, time.Minute, time.Minute)
	l.Fail("", "u")
	l.Fail("", "u")
	l.Succeed("", "u")
	l.Fail("", "u")
	if !l.Allowed("", "u") {
		t.Fatal("counter not reset after Succeed")
	}
	// Empty key always passes and never locks.
	for range 10 {
		l.Fail("", "")
	}
	if !l.Allowed("", "") {
		t.Fatal("an unkeyable caller was locked out")
	}
}

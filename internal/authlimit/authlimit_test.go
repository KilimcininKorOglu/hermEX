package authlimit

import (
	"testing"
	"time"
)

// TestLockoutAfterThreshold proves the limiter admits a key until the failure
// threshold, then refuses it for the lockout window, then admits it again once the
// cooldown elapses, the full brute-force-blunting cycle on an injected clock.
func TestLockoutAfterThreshold(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(3, time.Minute, 5*time.Minute)
	l.now = func() time.Time { return now }

	const key = "1.2.3.4"
	for i := range 2 {
		if !l.Allowed(key) {
			t.Fatalf("attempt %d refused early", i)
		}
		l.Fail(key)
	}
	// Third failure trips the lockout.
	if !l.Allowed(key) {
		t.Fatal("third attempt refused before threshold")
	}
	l.Fail(key)
	if l.Allowed(key) {
		t.Fatal("key not locked out after reaching threshold")
	}
	// Still locked one second before the cooldown ends.
	now = now.Add(5*time.Minute - time.Second)
	if l.Allowed(key) {
		t.Fatal("key admitted before lockout elapsed")
	}
	// Admitted once the cooldown passes.
	now = now.Add(2 * time.Second)
	if !l.Allowed(key) {
		t.Fatal("key still locked after cooldown elapsed")
	}
}

// TestSucceedClears proves a success wipes accrued failures so a user who finally
// types the right password is not held to a stale counter, and an empty key (an
// unkeyable caller) is never tracked.
func TestSucceedClears(t *testing.T) {
	l := New(3, time.Minute, time.Minute)
	l.Fail("u")
	l.Fail("u")
	l.Succeed("u")
	l.Fail("u")
	if !l.Allowed("u") {
		t.Fatal("counter not reset after Succeed")
	}
	// Empty key always passes and never locks.
	for range 10 {
		l.Fail("")
	}
	if !l.Allowed("") {
		t.Fatal("empty key was locked out")
	}
}

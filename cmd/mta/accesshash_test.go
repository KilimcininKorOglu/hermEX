package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"testing"

	"hermex/internal/directory"
)

// TestAccessHashSeesEveryRuleChange: the reloader swaps the allow/block list only
// when the hash moves, so an add, an edit of either field, and a delete must each
// give a different value, and the same rules must give the same value. The field
// separator keeps ("ab","c") apart from ("a","bc").
func TestAccessHashSeesEveryRuleChange(t *testing.T) {
	base := []directory.SenderRule{{Pattern: "spam.test", Action: "block"}, {Pattern: "ok.test", Action: "allow"}}
	sum := func(rules []directory.SenderRule) uint64 {
		t.Helper()
		h, err := accessHash(rules)
		if err != nil {
			t.Fatalf("accessHash: %v", err)
		}
		return h
	}
	want := sum(base)
	if again := sum([]directory.SenderRule{base[0], base[1]}); again != want {
		t.Errorf("the same rules hashed to %d and %d", want, again)
	}
	for name, rules := range map[string][]directory.SenderRule{
		"added":          append([]directory.SenderRule{{Pattern: "new.test", Action: "block"}}, base...),
		"deleted":        base[:1],
		"pattern edited": {{Pattern: "spam2.test", Action: "block"}, base[1]},
		"action edited":  {{Pattern: "spam.test", Action: "allow"}, base[1]},
		"field boundary": {{Pattern: "spam.testb", Action: "lock"}, base[1]},
	} {
		if got := sum(rules); got == want {
			t.Errorf("%s: hash unchanged (%d), the reloader would keep the old list", name, got)
		}
	}
}

// TestSRSKeyIsDerivedNotTheSecret: the SRS hash key is HMAC-SHA256(secret, "srs"),
// so the key-wrapping secret itself never leaves as the SRS key.
func TestSRSKeyIsDerivedNotTheSecret(t *testing.T) {
	secret := []byte("7f3a9c1e5b8d2f460a1c")
	got, err := srsKey(secret)
	if err != nil {
		t.Fatalf("srsKey: %v", err)
	}
	m := hmac.New(sha256.New, secret)
	if _, err := m.Write([]byte("srs")); err != nil {
		t.Fatalf("reference hmac: %v", err)
	}
	if want := m.Sum(nil); !bytes.Equal(got, want) {
		t.Errorf("srsKey = %x, want HMAC-SHA256(secret, \"srs\") = %x", got, want)
	}
	if bytes.Contains(got, secret) {
		t.Error("srsKey carries the secret itself")
	}
}

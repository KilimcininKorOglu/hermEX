package easpolicy

import "testing"

// TestKey proves the policy-generation token behaves as the staleness detector requires:
// an empty policy is the reserved baseline "1" (so an unconfigured server never forces
// re-provisioning), identical content always yields the same token regardless of map
// build order (so an unchanged device is not falsely flagged stale), a content change
// yields a different token (so a real change propagates), and a non-empty policy never
// collides with the reserved "0"/"1".
func TestKey(t *testing.T) {
	if Key(nil) != "1" || Key(Policy{}) != "1" {
		t.Fatalf("empty policy key = %q/%q, want the baseline \"1\"", Key(nil), Key(Policy{}))
	}

	a := Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 8}
	b := Policy{}
	b["MinDevicePasswordLength"] = 8
	b["DevicePasswordEnabled"] = 1
	if Key(a) != Key(b) {
		t.Errorf("same content built in different order gave different keys: %q vs %q", Key(a), Key(b))
	}

	changed := Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 6}
	if Key(a) == Key(changed) {
		t.Errorf("a value change did not change the key (%q) — a policy edit would not propagate", Key(a))
	}
	if k := Key(a); k == "0" || k == "1" {
		t.Errorf("a non-empty policy collided with a reserved key: %q", k)
	}
}

// TestMergeOverrideWins proves the two-layer resolution: a mailbox override replaces
// the global default for the fields it sets, the default supplies the rest, and neither
// input is mutated. Getting this wrong would serve a device the wrong security policy.
func TestMergeOverrideWins(t *testing.T) {
	base := Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 4, "AllowCamera": 1}
	override := Policy{"MinDevicePasswordLength": 8, "AllowBluetooth": 0}

	got := Merge(base, override)
	want := Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 8, "AllowCamera": 1, "AllowBluetooth": 0}
	if len(got) != len(want) {
		t.Fatalf("merged = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("merged[%q] = %d, want %d", k, got[k], v)
		}
	}
	// Inputs untouched.
	if base["MinDevicePasswordLength"] != 4 || len(override) != 2 {
		t.Errorf("Merge mutated an input: base=%v override=%v", base, override)
	}
}

// TestMergeNilLayers proves a nil default or override contributes nothing.
func TestMergeNilLayers(t *testing.T) {
	only := Policy{"DevicePasswordEnabled": 1}
	if got := Merge(nil, only); len(got) != 1 || got["DevicePasswordEnabled"] != 1 {
		t.Errorf("Merge(nil, p) = %v, want the override", got)
	}
	if got := Merge(only, nil); len(got) != 1 || got["DevicePasswordEnabled"] != 1 {
		t.Errorf("Merge(p, nil) = %v, want the base", got)
	}
}

// TestValidateRejectsUnknownField proves a misspelled or unsupported field is refused
// so it cannot be stored and then silently dropped at provisioning time.
func TestValidateRejectsUnknownField(t *testing.T) {
	if err := (Policy{"DevicePasswordEnabled": 1}).Validate(); err != nil {
		t.Errorf("a canonical field was rejected: %v", err)
	}
	if err := (Policy{"DeviceEncryptionEnabled": 1}).Validate(); err == nil {
		t.Error("the deprecated/renamed field name was accepted; want rejected")
	}
	if err := (Policy{"NotAPolicyField": 1}).Validate(); err == nil {
		t.Error("an unknown field was accepted; want rejected")
	}
}

// TestValidateRejectsOutOfRange proves a grossly invalid value is refused before it can
// reach a device: a non-boolean toggle and a negative numeric limit, while in-range
// values (including a numeric 0) pass.
func TestValidateRejectsOutOfRange(t *testing.T) {
	if err := (Policy{"DevicePasswordEnabled": 2}).Validate(); err == nil {
		t.Error("a toggle value of 2 was accepted; want rejected")
	}
	if err := (Policy{"MinDevicePasswordLength": -1}).Validate(); err == nil {
		t.Error("a negative numeric limit was accepted; want rejected")
	}
	if err := (Policy{"DevicePasswordEnabled": 0, "MinDevicePasswordLength": 0, "MaxInactivityTimeDeviceLock": 900}).Validate(); err != nil {
		t.Errorf("in-range values were rejected: %v", err)
	}
}

// TestFieldsCoverTokens is a guard that the canonical field set stays the expected
// size, so a field dropped in a refactor is caught rather than silently un-enforced.
func TestFieldsCoverTokens(t *testing.T) {
	if len(Fields) != 40 {
		t.Errorf("Fields has %d entries, want 40 (the modeled EASProvisionDoc scalar set)", len(Fields))
	}
	if len(known) != len(Fields) {
		t.Errorf("a duplicate field name collapsed the index: %d indexed, %d listed", len(known), len(Fields))
	}
}

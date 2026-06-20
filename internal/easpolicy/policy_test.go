package easpolicy

import "testing"

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

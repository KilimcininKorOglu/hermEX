package easpolicy

import (
	"hash/fnv"
	"strconv"
	"testing"
)

// TestKeyIsStandardFNV1a pins the token to 32-bit FNV-1a over the canonical
// "name=value;" text, the function every issued key was computed with. Devices
// hold those keys, so a different hash would mark every provisioned device stale
// on upgrade. The expected value comes from the standard library's FNV-1a.
func TestKeyIsStandardFNV1a(t *testing.T) {
	p := Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 6}
	text := ""
	for _, f := range Fields {
		if v, ok := p[f.Name]; ok {
			text += f.Name + "=" + strconv.Itoa(int(v)) + ";"
		}
	}
	h := fnv.New32a()
	if _, err := h.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	want := h.Sum32()
	if want <= 1 {
		want += 2
	}
	if got := Key(p); got != strconv.FormatUint(uint64(want), 10) {
		t.Errorf("Key = %s, want the FNV-1a token %d of %q", got, want, text)
	}
}

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
		t.Errorf("a value change did not change the key (%q), a policy edit would not propagate", Key(a))
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
	if err := (Policy{"DevicePasswordEnabled": 0, "MinDevicePasswordLength": 1, "MaxInactivityTimeDeviceLock": 900}).Validate(); err != nil {
		t.Errorf("in-range values were rejected: %v", err)
	}
}

// TestValidateHoldsTheSpecValueSets checks each numeric field whose [MS-ASPROV]
// 2.2.2 element page states a value set: both edges pass, the value just outside
// each edge is refused, and an enumeration refuses a gap inside its span. The
// expected sets are the element pages' own tables, not the code's.
func TestValidateHoldsTheSpecValueSets(t *testing.T) {
	cases := []struct {
		field string
		ok    []int
		bad   []int
	}{
		{"AllowBluetooth", []int{0, 2}, []int{-1, 3}},
		{"MinDevicePasswordLength", []int{1, 16}, []int{0, 17}},
		{"MaxDevicePasswordFailedAttempts", []int{4, 16}, []int{3, 17}},
		{"MinDevicePasswordComplexCharacters", []int{1, 4}, []int{0, 5}},
		{"MaxCalendarAgeFilter", []int{0, 4, 7}, []int{1, 3, 8}},
		{"MaxEmailAgeFilter", []int{0, 5}, []int{-1, 6}},
		{"RequireSignedSMIMEAlgorithm", []int{0, 1}, []int{-1, 2}},
		{"RequireEncryptionSMIMEAlgorithm", []int{0, 4}, []int{-1, 5}},
		{"AllowSMIMEEncryptionAlgorithmNegotiation", []int{0, 2}, []int{-1, 3}},
		{"MaxEmailBodyTruncationSize", []int{-1, 0, 51200}, []int{-2}},
		{"MaxEmailHTMLBodyTruncationSize", []int{-1, 0, 51200}, []int{-2}},
		{"DevicePasswordHistory", []int{0, 50}, []int{-1}},
	}
	for _, c := range cases {
		for _, v := range c.ok {
			if err := (Policy{c.field: v}).Validate(); err != nil {
				t.Errorf("%s = %d was refused: %v", c.field, v, err)
			}
		}
		for _, v := range c.bad {
			if err := (Policy{c.field: v}).Validate(); err == nil {
				t.Errorf("%s = %d was accepted; it is outside the spec's value set", c.field, v)
			}
		}
	}
}

// TestCloneIsIndependentAndDropsEmpty proves a stored policy is a copy the caller
// cannot change afterwards (an edit to the source must not rewrite what was saved),
// and that an empty policy clones to nil so storage never persists an empty object.
func TestCloneIsIndependentAndDropsEmpty(t *testing.T) {
	if Policy(nil).Clone() != nil || (Policy{}).Clone() != nil {
		t.Error("an empty policy cloned to a non-nil map, want nil")
	}
	src := Policy{"DevicePasswordEnabled": 1}
	c := src.Clone()
	src["DevicePasswordEnabled"] = 0
	src["AllowCamera"] = 0
	if len(c) != 1 || c["DevicePasswordEnabled"] != 1 {
		t.Errorf("clone = %v after the source changed, want {DevicePasswordEnabled:1}", c)
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

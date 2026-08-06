package directory

import "testing"

// galSet is the four visibility cases an operator can produce with the panel's
// hide switches.
func galSet() []GALEntry {
	return []GALEntry{
		{Address: "open@hermex.test"},
		{Address: "nogal@hermex.test", HiddenFrom: HideFromGAL},
		{Address: "noanr@hermex.test", HiddenFrom: HideFromResolve},
		{Address: "nolist@hermex.test", HiddenFrom: HideFromAL},
	}
}

func addresses(entries []GALEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Address)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestVisibleGALDropsOnlyGALHidden proves a browse or search surface hides the
// users withheld from the address list itself, and keeps a user withheld only
// from name resolution, which is what the address-book layer already serves for a
// GAL browse.
func TestVisibleGALDropsOnlyGALHidden(t *testing.T) {
	got := addresses(VisibleGAL(galSet()))
	if contains(got, "nogal@hermex.test") {
		t.Error("an address hidden from the GAL was listed")
	}
	if !contains(got, "noanr@hermex.test") {
		t.Error("an address hidden only from name resolution was dropped from a browse")
	}
	if !contains(got, "open@hermex.test") || !contains(got, "nolist@hermex.test") {
		t.Errorf("a visible address was dropped: %v", got)
	}
}

// TestResolvableGALDropsBoth proves name resolution withholds both the addresses
// hidden from the address list and those hidden from resolution.
func TestResolvableGALDropsBoth(t *testing.T) {
	got := addresses(ResolvableGAL(galSet()))
	if contains(got, "nogal@hermex.test") {
		t.Error("an address hidden from the GAL was resolvable")
	}
	if contains(got, "noanr@hermex.test") {
		t.Error("an address hidden from name resolution was resolvable")
	}
	if !contains(got, "open@hermex.test") {
		t.Errorf("a visible address was dropped: %v", got)
	}
}

// TestFiltersLeaveTheInputAlone proves the filters return a new slice, so a
// caller that keeps the original set is unaffected, and that nil stays nil.
func TestFiltersLeaveTheInputAlone(t *testing.T) {
	in := galSet()
	VisibleGAL(in)
	ResolvableGAL(in)
	if len(in) != 4 {
		t.Errorf("input length = %d after filtering, want 4 untouched", len(in))
	}
	if VisibleGAL(nil) != nil || ResolvableGAL(nil) != nil {
		t.Error("filtering nil should stay nil")
	}
}

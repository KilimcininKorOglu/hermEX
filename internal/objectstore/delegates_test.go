package objectstore

import (
	"slices"
	"testing"
)

// TestDelegatesRoundTrip proves the public-delegate list persists through the
// store-root property, reads back as no delegates on a fresh mailbox, and clears
// on an empty set. NSPI and the admin console both manage delegation through this
// list, so a silent drop would let a removed delegate keep acting or hide a
// granted one — order is preserved because the list is the address book's row order.
func TestDelegatesRoundTrip(t *testing.T) {
	s := openTestStore(t)

	got, err := s.GetDelegates()
	if err != nil {
		t.Fatalf("GetDelegates on a fresh store: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("fresh store delegates = %v, want none", got)
	}

	want := []string{"boss@hermex.test", "assistant@hermex.test"}
	if err := s.SetDelegates(want); err != nil {
		t.Fatalf("SetDelegates: %v", err)
	}
	got, err = s.GetDelegates()
	if err != nil {
		t.Fatalf("GetDelegates after set: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("delegates = %v, want %v", got, want)
	}

	if err := s.SetDelegates(nil); err != nil {
		t.Fatalf("SetDelegates(nil): %v", err)
	}
	got, err = s.GetDelegates()
	if err != nil {
		t.Fatalf("GetDelegates after clear: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("cleared delegates = %v, want none", got)
	}
}

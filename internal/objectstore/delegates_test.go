package objectstore

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

// TestUpdateDelegatesLosesNoAddition proves two writers adding delegates at once,
// through two handles on one mailbox as two daemons would (EWS and the address
// book), both land on the list. A read-modify-write outside one transaction lets
// one writer's list, read before the other's commit, overwrite that commit.
func TestUpdateDelegatesLosesNoAddition(t *testing.T) {
	dir := t.TempDir()
	const writers, rounds = 2, 20
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for w := range writers {
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer mustClose(t, st)
		wg.Go(func() {
			for r := range rounds {
				addr := fmt.Sprintf("d%d-%d@hermex.test", w, r)
				errs <- st.UpdateDelegates(func(list []string) []string { return append(list, addr) })
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	list, err := st.GetDelegates()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != writers*rounds {
		t.Errorf("delegates = %d after %d additions", len(list), writers*rounds)
	}
}

// TestDelegatesRoundTrip proves the public-delegate list persists through the
// store-root property, reads back as no delegates on a fresh mailbox, and clears
// on an empty set. NSPI and the admin console both manage delegation through this
// list, so a silent drop would let a removed delegate keep acting or hide a
// granted one, order is preserved because the list is the address book's row order.
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

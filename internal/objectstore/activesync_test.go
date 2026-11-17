package objectstore

import (
	"strconv"
	"sync"
	"testing"
)

// TestUpdateActiveSyncStateLosesNoWrite proves a read-modify-write through
// UpdateActiveSyncState is atomic against another writer. Two handles on one
// mailbox stand for two processes (the ActiveSync daemon and the admin panel);
// each increments a counter many times at once. A write made between another
// writer's read and its write would be erased, and the count would come up short.
func TestUpdateActiveSyncStateLosesNoWrite(t *testing.T) {
	dir := t.TempDir()
	const writers, rounds = 2, 40
	stores := make([]*Store, writers)
	for i := range stores {
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		stores[i] = st
	}
	increment := func(cur string) (string, bool, error) {
		n, _ := strconv.Atoi(cur)
		return strconv.Itoa(n + 1), true, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for _, st := range stores {
		wg.Add(1)
		go func(st *Store) {
			defer wg.Done()
			for range rounds {
				errs <- st.UpdateActiveSyncState(increment)
			}
		}(st)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := stores[0].GetActiveSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if want := strconv.Itoa(writers * rounds); got != want {
		t.Errorf("counter = %s after %s increments", got, want)
	}
}

// TestUpdateActiveSyncStateSkipsAnUnchangedWrite proves an update reporting no
// change writes nothing, so it cannot replace a value with a stale copy.
func TestUpdateActiveSyncStateSkipsAnUnchangedWrite(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetActiveSyncDevices("kept"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateActiveSyncDevices(func(string) (string, bool, error) { return "dropped", false, nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetActiveSyncDevices(); got != "kept" {
		t.Errorf("devices = %q after an update that reported no change, want kept", got)
	}
}

package relay

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestDaemonsOpeningAFreshSpoolAtOnceAllSucceed starts several daemons against a
// spool file that does not exist yet, as a fresh install starts every daemon at
// once. Each must open it: a daemon that fails here exits at startup.
func TestDaemonsOpeningAFreshSpoolAtOnceAllSucceed(t *testing.T) {
	for round := range 20 {
		path := filepath.Join(t.TempDir(), "relay.sqlite3")
		var wg sync.WaitGroup
		errs := make([]error, 7)
		start := make(chan struct{})
		for i := range errs {
			wg.Go(func() {
				<-start
				s, err := Open(path)
				if err == nil {
					err = s.Close()
				}
				errs[i] = err
			})
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d daemon %d: %v", round, i, err)
			}
		}
	}
}

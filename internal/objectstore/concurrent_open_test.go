package objectstore

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestConcurrentFirstOpensOfAMailboxAllSucceed opens a mailbox that does not exist
// yet from several handles at once, as two deliveries racing to a fresh account
// do. Each open must succeed: a failed one temp-fails the delivery.
func TestConcurrentFirstOpensOfAMailboxAllSucceed(t *testing.T) {
	for round := range 10 {
		dir := filepath.Join(t.TempDir(), "mbox")
		var wg sync.WaitGroup
		errs := make([]error, 4)
		start := make(chan struct{})
		for i := range errs {
			wg.Go(func() {
				<-start
				s, err := Open(dir)
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
				t.Fatalf("round %d opener %d: %v", round, i, err)
			}
		}
	}
}

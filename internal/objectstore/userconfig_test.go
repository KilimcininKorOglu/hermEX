package objectstore

import (
	"fmt"
	"sync"
	"testing"
)

// TestUpdateUserConfigsLosesNoConfig proves two writers creating differently named
// user configurations at once, through two handles on one mailbox as two EWS
// requests would, both keep theirs. MS-OXWSUSRCFG identifies a configuration by
// folder and name, so creating one must never remove another.
func TestUpdateUserConfigsLosesNoConfig(t *testing.T) {
	dir := t.TempDir()
	const writers, rounds = 2, 20
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for w := range writers {
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		wg.Go(func() {
			for r := range rounds {
				rec := UserConfig{FID: 13, Name: fmt.Sprintf("cfg%d-%d", w, r)}
				errs <- st.UpdateUserConfigs(func(recs []UserConfig) ([]UserConfig, bool, error) {
					return append(recs, rec), true, nil
				})
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
	defer st.Close()
	recs, err := st.GetUserConfigs()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != writers*rounds {
		t.Errorf("configs = %d after %d creations", len(recs), writers*rounds)
	}
}

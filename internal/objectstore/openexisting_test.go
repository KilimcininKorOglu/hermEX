package objectstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"hermex/internal/mapi"
)

// TestOpenExistingRefusesAnAbsentStore is the guard a maintenance pass depends
// on. Open CREATES the store, so a pass run where the mailbox path does not
// resolve would leave an empty mailbox behind and report success over work it
// never did.
func TestOpenExistingRefusesAnAbsentStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nothing-here")

	st, err := OpenExisting(dir)
	if !errors.Is(err, ErrNotProvisioned) {
		if st != nil {
			st.Close()
		}
		t.Fatalf("OpenExisting on an absent store = %v, want ErrNotProvisioned", err)
	}
	// And it created nothing on the way to saying so.
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused open left %s behind", dir)
	}
}

// TestOpenExistingOpensAProvisionedStore keeps the guard from refusing the
// mailboxes it is meant to work on.
func TestOpenExistingOpensAProvisionedStore(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	again, err := OpenExisting(dir)
	if err != nil {
		t.Fatalf("OpenExisting on a provisioned store: %v", err)
	}
	defer again.Close()
	if _, err := again.ListMessages(int64(mapi.PrivateFIDInbox)); err != nil {
		t.Errorf("the reopened store does not read: %v", err)
	}
}

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
			mustClose(t, st)
		}
		t.Fatalf("OpenExisting on an absent store = %v, want ErrNotProvisioned", err)
	}
	// And it created nothing on the way to saying so.
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused open left %s behind", dir)
	}
}

// TestOpenExistingRefusesAnEmptyDirectory: a directory that exists but holds no
// store (made by hand, or left by a failed provisioning) is not a mailbox, so the
// guard refuses it and writes no database into it.
func TestOpenExistingRefusesAnEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if st, err := OpenExisting(dir); !errors.Is(err, ErrNotProvisioned) {
		if st != nil {
			mustClose(t, st)
		}
		t.Fatalf("OpenExisting on an empty directory = %v, want ErrNotProvisioned", err)
	}
	entries, err := os.ReadDir(dir)
	mustNoErr(t, "read the directory", err)
	if len(entries) != 0 {
		t.Errorf("the refused open wrote %d entries into the directory", len(entries))
	}
}

// TestOpenPublicExistingRefusesAnUnprovisionedDomain: a domain whose public
// folders were never set up reads as absent, both for a missing path and for an
// empty directory, and the read seeds no public store as a side effect; a
// provisioned public store still opens.
func TestOpenPublicExistingRefusesAnUnprovisionedDomain(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-here")
	empty := t.TempDir()
	for _, dir := range []string{missing, empty} {
		if st, err := OpenPublicExisting(dir); !errors.Is(err, ErrNotProvisioned) {
			if st != nil {
				mustClose(t, st)
			}
			t.Errorf("OpenPublicExisting(%s) = %v, want ErrNotProvisioned", dir, err)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Error("the refused open created the missing directory")
	}
	if _, err := os.Stat(filepath.Join(empty, "objects.sqlite3")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the refused open seeded a public store")
	}

	pub, err := OpenPublic(t.TempDir())
	mustNoErr(t, "provision a public store", err)
	dir := pub.Dir()
	mustNoErr(t, "close", pub.Close())
	again, err := OpenPublicExisting(dir)
	mustNoErr(t, "reopen the provisioned public store", err)
	mustNoErr(t, "close", again.Close())
}

// TestOpenExistingOpensAProvisionedStore keeps the guard from refusing the
// mailboxes it is meant to work on.
func TestOpenExistingOpensAProvisionedStore(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)

	again, err := OpenExisting(dir)
	if err != nil {
		t.Fatalf("OpenExisting on a provisioned store: %v", err)
	}
	defer mustClose(t, again)
	if _, err := again.ListMessages(int64(mapi.PrivateFIDInbox)); err != nil {
		t.Errorf("the reopened store does not read: %v", err)
	}
}

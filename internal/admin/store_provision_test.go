package admin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"hermex/internal/objectstore"
)

// notCreated fails the test when a store file appeared at dir.
func notCreated(t *testing.T, dir, what string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "objects.sqlite3")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s created a mailbox store at %s (stat err %v)", what, dir, err)
	}
}

// TestMailboxReadsDoNotProvision proves the admin reads of a mailbox that was
// never provisioned answer empty settings and leave no store behind. A read that
// created one gave a user an empty mailbox by opening their detail page.
func TestMailboxReadsDoNotProvision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never")
	ms := mailboxStore{}
	q, used, err := ms.GetQuota(dir)
	mustNoErr(t, err, "read the quota")
	wantEq(t, q, objectstore.QuotaLimits{}, "the quota of an unprovisioned mailbox")
	wantEq(t, used, int64(0), "the usage of an unprovisioned mailbox")
	_, err = ms.GetOOFSettings(dir)
	mustNoErr(t, err, "read the out-of-office settings")
	devs, err := ms.ListDevices(dir)
	mustNoErr(t, err, "list the devices")
	wantEq(t, len(devs), 0, "the devices of an unprovisioned mailbox")
	_, err = ms.ListFolders(dir)
	mustNoErr(t, err, "list the folders")
	notCreated(t, dir, "a read")

	if err := ms.WipeDevice(dir, "dev1", false); !errors.Is(err, objectstore.ErrNotProvisioned) {
		t.Errorf("a device action on an unprovisioned mailbox = %v, want ErrNotProvisioned", err)
	}
	notCreated(t, dir, "a device action")

	// A mailbox is made when its account is created, so a settings write to one
	// that is missing is an error; writing it would replace a lost mailbox with an
	// empty one.
	if err := ms.SetQuota(dir, objectstore.QuotaLimits{StorageKB: 1024}); !errors.Is(err, objectstore.ErrNotProvisioned) {
		t.Errorf("a settings write to an unprovisioned mailbox = %v, want ErrNotProvisioned", err)
	}
	if err := ms.SetOOFSettings(dir, objectstore.OOFSettings{}); !errors.Is(err, objectstore.ErrNotProvisioned) {
		t.Errorf("an OOF write to an unprovisioned mailbox = %v, want ErrNotProvisioned", err)
	}
	notCreated(t, dir, "a settings write")
}

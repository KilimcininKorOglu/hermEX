package publicfolder

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"hermex/internal/objectstore"
)

// TestADomainCannotNameAPathOutsideTheDomainRoot passes domain names that are
// paths: the admin API takes the domain from a query parameter, and the public
// store directory is the domain root joined with that name. A name with a path
// separator or a dot segment must open, create and list nothing, and above all
// not another directory's store, here a private mailbox beside the domain root.
func TestADomainCannotNameAPathOutsideTheDomainRoot(t *testing.T) {
	root := t.TempDir()
	paths := fakePaths{root: root}
	svc := New(paths)
	victim := filepath.Join(root, "mail")
	st, err := objectstore.OpenPublic(victim)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	for _, bad := range []string{"../mail", "..", "a/../../mail", `..\mail`, "/etc", ".hidden"} {
		if st, err := svc.OpenForDomain(bad); err == nil {
			st.Close()
			t.Errorf("OpenForDomain(%q) opened a store, want it refused", bad)
		} else if !errors.Is(err, ErrBadDomain) {
			t.Errorf("OpenForDomain(%q) err = %v, want ErrBadDomain", bad, err)
		}
		if folders, err := svc.Folders(bad); err == nil || len(folders) != 0 {
			t.Errorf("Folders(%q) = %d folders, err %v, want it refused", bad, len(folders), err)
		}
		if err := svc.Provision(bad); err == nil {
			t.Errorf("Provision(%q) succeeded, want it refused", bad)
		}
		if _, err := svc.CreateFolder(bad, "x"); err == nil {
			t.Errorf("CreateFolder(%q) succeeded, want it refused", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "domain")); !os.IsNotExist(err) {
		t.Errorf("a refused name created the domain root (err %v)", err)
	}
}

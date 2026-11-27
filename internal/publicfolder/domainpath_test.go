package publicfolder

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestADomainCannotNameAPathOutsideTheDomainRoot passes domain names that are
// paths, empty, contain null bytes, or exceed the RFC 1035 253-octet domain
// name limit: the admin API takes the domain from a query parameter, and the
// public store directory is the domain root joined with that name. A rejected
// name must open, create, modify, delete, and list nothing, and above all leave
// another directory's store untouched, here a store beside the domain root.
func TestADomainCannotNameAPathOutsideTheDomainRoot(t *testing.T) {
	root := t.TempDir()
	paths := fakePaths{root: root}
	svc := New(paths)
	victim := filepath.Join(root, "mail")
	st, err := objectstore.OpenPublic(victim)
	mustNoErr(t, err, "OpenPublic(victim)")
	victimFID := createFolder(t, st, "VictimSecret")
	grantAnyone(t, st, victimFID, mapi.FrightsVisible|mapi.FrightsReadAny)
	beforeFolders, err := st.ListFolders()
	mustNoErr(t, err, "ListFolders(victim before)")
	beforePerms, err := st.ListPermissions(victimFID)
	mustNoErr(t, err, "ListPermissions(victim before)")
	mustClose(t, st)

	oversized := strings.Repeat("a", 250) + ".test"
	badDomains := []string{
		"../mail", "..", "a/../../mail", `..\mail`, "/etc", ".hidden",
		"", "   ", "a\x00b.test", oversized,
	}
	for _, bad := range badDomains {
		refuseAdminOps(t, svc, bad, victimFID)
		refuseCallerOps(t, svc, "u@"+bad)
	}
	if _, err := os.Stat(filepath.Join(root, "domain")); !os.IsNotExist(err) {
		t.Errorf("a refused name created the domain root (err %v)", err)
	}

	// Verify the victim store beside the domain root was not modified.
	st, err = objectstore.OpenPublicExisting(victim)
	mustNoErr(t, err, "reopen victim")
	defer mustClose(t, st)
	afterFolders, err := st.ListFolders()
	mustNoErr(t, err, "ListFolders(victim after)")
	if !slices.Equal(afterFolders, beforeFolders) {
		t.Errorf("victim folders changed: got %+v, want %+v", afterFolders, beforeFolders)
	}
	afterPerms, err := st.ListPermissions(victimFID)
	mustNoErr(t, err, "ListPermissions(victim after)")
	if !slices.Equal(afterPerms, beforePerms) {
		t.Errorf("victim permissions changed: got %+v, want %+v", afterPerms, beforePerms)
	}
}

// refuseAdminOps asserts every management call refuses the domain name bad with
// ErrBadDomain and returns nothing.
func refuseAdminOps(t *testing.T, svc *Service, bad string, fid int64) {
	t.Helper()
	if st, err := svc.OpenForDomain(bad); err == nil {
		mustClose(t, st)
		t.Errorf("OpenForDomain(%q) opened a store, want it refused", bad)
	} else if !errors.Is(err, ErrBadDomain) {
		t.Errorf("OpenForDomain(%q) err = %v, want ErrBadDomain", bad, err)
	}
	if folders, err := svc.Folders(bad); !errors.Is(err, ErrBadDomain) || len(folders) != 0 {
		t.Errorf("Folders(%q) = %d folders, err %v, want ErrBadDomain and 0 folders", bad, len(folders), err)
	}
	if err := svc.Provision(bad); !errors.Is(err, ErrBadDomain) {
		t.Errorf("Provision(%q) = %v, want ErrBadDomain", bad, err)
	}
	if fid, err := svc.CreateFolder(bad, "x"); !errors.Is(err, ErrBadDomain) || fid != 0 {
		t.Errorf("CreateFolder(%q) = %d, %v, want 0 and ErrBadDomain", bad, fid, err)
	}
	if err := svc.DeleteFolder(bad, fid); !errors.Is(err, ErrBadDomain) {
		t.Errorf("DeleteFolder(%q) = %v, want ErrBadDomain", bad, err)
	}
	if err := svc.Grant(bad, fid, objectstore.PermissionChange{
		Op: objectstore.PermAdd, Username: "attacker@evil.test", Rights: mapi.FrightsVisible | mapi.FrightsCreate,
	}); !errors.Is(err, ErrBadDomain) {
		t.Errorf("Grant(%q) = %v, want ErrBadDomain", bad, err)
	}
}

// refuseCallerOps asserts a caller address whose domain is not a domain name
// resolves to no directory and opens or lists nothing.
func refuseCallerOps(t *testing.T, svc *Service, caller string) {
	t.Helper()
	if dir := svc.DirForCaller(caller); dir != "" {
		t.Errorf("DirForCaller(%q) = %q, want empty", caller, dir)
	}
	openNothing(t, svc, caller)
	if folders, err := svc.VisibleFolders(caller); err != nil || len(folders) != 0 {
		t.Errorf("VisibleFolders(%q) = %d folders, err %v, want 0, nil", caller, len(folders), err)
	}
}

// openNothing asserts OpenForCaller opens no store for caller.
func openNothing(t *testing.T, svc *Service, caller string) {
	t.Helper()
	st, ok, err := svc.OpenForCaller(caller)
	if st != nil {
		mustClose(t, st)
	}
	if st != nil || ok || err != nil {
		t.Errorf("OpenForCaller(%q) = %v, %v, %v, want nothing opened", caller, st, ok, err)
	}
}

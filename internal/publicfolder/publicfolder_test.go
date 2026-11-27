package publicfolder

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// fakePaths maps each domain to its own directory under a test root, so per-domain
// public stores are physically separate exactly as in production, where HomedirFor
// derives a distinct path per domain.
type fakePaths struct{ root string }

func (p fakePaths) HomedirFor(domain string) string {
	return filepath.Join(p.root, "domain", domain)
}

// TestCallerDomainIsTakenAfterTheLastAt routes a caller whose local part holds an
// '@'. RFC 5321 4.1.2 allows one in a quoted local part, the directory accepts
// it, and directory.ValidateAddress assigns the account to the domain after the
// LAST '@'. The public store must be that same domain's, or the account reads a
// store keyed on a domain it does not belong to.
func TestCallerDomainIsTakenAfterTheLastAt(t *testing.T) {
	paths := fakePaths{root: t.TempDir()}
	svc := New(paths)
	got := svc.DirForCaller("x@other.test@Hermex.test")
	if want := paths.HomedirFor("hermex.test"); got != want {
		t.Errorf("DirForCaller = %s, want %s", got, want)
	}
	for _, bad := range []string{"", "nodomain", "x@"} {
		if dir := svc.DirForCaller(bad); dir != "" {
			t.Errorf("DirForCaller(%q) = %q, want empty", bad, dir)
		}
	}
}

// TestAdminOpsNeverProvisionOrDeleteTheSkeleton covers the management calls on
// hostile input. A read (Folders) or a grant on a domain with no public store
// must not create one as a side effect; a delete naming a structural folder id
// (the IPM subtree and the rest of the skeleton) is refused before the store is
// touched; and a caller address without a domain opens nothing.
func TestAdminOpsNeverProvisionOrDeleteTheSkeleton(t *testing.T) {
	paths := fakePaths{root: t.TempDir()}
	svc := New(paths)

	folders, err := svc.Folders("unprov.test")
	mustNoErr(t, err, "Folders on an un-provisioned domain")
	wantEq(t, len(folders), 0, "folders of an un-provisioned domain")
	if err := svc.Grant("unprov.test", int64(mapi.PublicFIDUnassignedStart), objectstore.PermissionChange{Op: objectstore.PermAdd, Username: "a@unprov.test", Rights: mapi.FrightsVisible}); !errors.Is(err, objectstore.ErrNotProvisioned) {
		t.Errorf("Grant on an un-provisioned domain = %v, want ErrNotProvisioned", err)
	}
	if _, err := os.Stat(paths.HomedirFor("unprov.test")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a read or grant created the domain's public store (stat err %v)", err)
	}

	refuseStructuralDeletes(t, svc, "local.test")
	if err := svc.DeleteFolder("unprov.test", int64(mapi.PublicFIDUnassignedStart)); !errors.Is(err, objectstore.ErrNotProvisioned) {
		t.Errorf("DeleteFolder on an un-provisioned domain = %v, want ErrNotProvisioned", err)
	}
	if _, err := os.Stat(paths.HomedirFor("unprov.test")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("DeleteFolder created the un-provisioned domain's public store (stat err %v)", err)
	}

	for _, caller := range []string{"", "nodomain", "x@"} {
		openNothing(t, svc, caller)
	}
}

// refuseStructuralDeletes provisions domain, asks to delete every structural id
// and checks each is refused and the folder list is unchanged.
func refuseStructuralDeletes(t *testing.T, svc *Service, domain string) {
	t.Helper()
	mustNoErr(t, svc.Provision(domain), "Provision")
	before, err := svc.Folders(domain)
	mustNoErr(t, err, "Folders before structural deletes")
	for _, fid := range []int64{-1, 0, 1, int64(mapi.PublicFIDUnassignedStart) - 1} {
		if err := svc.DeleteFolder(domain, fid); !errors.Is(err, ErrStructuralFolder) {
			t.Errorf("DeleteFolder(%d) = %v, want ErrStructuralFolder", fid, err)
		}
	}
	after, err := svc.Folders(domain)
	mustNoErr(t, err, "Folders after structural deletes")
	if !slices.EqualFunc(before, after, func(a, b FolderWithGrants) bool {
		return a.ID == b.ID && a.DisplayName == b.DisplayName
	}) {
		t.Errorf("folders changed after refused structural deletes: before %+v, after %+v", before, after)
	}
}

func grantUser(t *testing.T, st *objectstore.Store, fid int64, username string, rights uint32) {
	t.Helper()
	if err := st.ModifyPermissions(fid, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: username, Rights: rights},
	}); err != nil {
		t.Fatal(err)
	}
}

func grantAnyone(t *testing.T, st *objectstore.Store, fid int64, rights uint32) {
	t.Helper()
	if err := st.ModifyPermissions(fid, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, MemberID: mapi.MemberIDDefault, Rights: rights},
	}); err != nil {
		t.Fatal(err)
	}
}

func names(fs []Folder) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.DisplayName
	}
	return out
}

// TestVisibleFoldersACLAndTenantIsolation proves the two invariants the whole
// public-folder feature rests on: visibility is gated by the per-folder ACL
// (anyone-grant vs explicit-grant vs none), and a caller only ever reaches their
// own domain's store, so one domain's folders never leak to another's users.
func TestVisibleFoldersACLAndTenantIsolation(t *testing.T) {
	paths := fakePaths{root: t.TempDir()}
	svc := New(paths)

	checkUnprovisionedDomain(t, svc, paths)
	seedLocalTree(t, svc)

	// Reader holds no explicit grant: sees only the anyone-granted Announcements,
	// never Staff (which has no anyone grant), and has no post right.
	reader := visibleTo(t, svc, "reader@local.test")
	wantNames(t, reader, []string{"Announcements"},
		"what a reader with no explicit grant sees")
	if len(reader) == 1 && reader[0].Rights&mapi.FrightsCreate != 0 {
		t.Errorf("reader unexpectedly has FrightsCreate on Announcements: rights=%#x", reader[0].Rights)
	}

	// Poster holds explicit grants: sees both, with post rights on Announcements.
	poster := visibleTo(t, svc, "poster@local.test")
	wantNames(t, poster, []string{"Announcements", "Staff"}, "what a poster with explicit grants sees")
	wantPostRight(t, poster, "Announcements")

	// Caller address matching is case-insensitive.
	wantNames(t, visibleTo(t, svc, "POSTER@LOCAL.TEST"), []string{"Announcements", "Staff"},
		"what an uppercase poster email sees")

	seedOtherTree(t, svc)
	// Tenant isolation: the second domain's caller sees only their domain's folder
	// and never local.test's tree, even though local.test's store exists on disk. The
	// domain is derived from the caller's address, so there is no path that opens
	// another tenant's store.
	wantNames(t, visibleTo(t, svc, "intruder@other.test"), []string{"OtherAnnounce"},
		"what an other.test caller sees")
}

// TestAdminFolderLifecycleAndGrants verifies CreateFolder, Grant, Folders,
// Provision idempotency, MS-OXCPERM visibility gating (FrightsReadAny without
// FrightsVisible must not expose a folder), and DeleteFolder on an admin-created
// folder.
func TestAdminFolderLifecycleAndGrants(t *testing.T) {
	svc := New(fakePaths{root: t.TempDir()})

	fid, err := svc.CreateFolder("mgmt.test", "Shared")
	mustNoErr(t, err, "CreateFolder(mgmt.test, Shared)")
	if fid < int64(mapi.PublicFIDUnassignedStart) {
		t.Fatalf("CreateFolder id = %d, want >= %d", fid, mapi.PublicFIDUnassignedStart)
	}

	// Grant read-only without FrightsVisible: per MS-OXCPERM, a folder is hidden
	// unless FrightsVisible is set.
	mustNoErr(t, svc.Grant("mgmt.test", fid, objectstore.PermissionChange{
		Op: objectstore.PermAdd, Username: "hidden@mgmt.test", Rights: mapi.FrightsReadAny,
	}), "Grant read without visible")
	wantEq(t, len(visibleTo(t, svc, "hidden@mgmt.test")), 0, "folder without FrightsVisible")

	// Grant FrightsVisible to member@mgmt.test.
	mustNoErr(t, svc.Grant("mgmt.test", fid, objectstore.PermissionChange{
		Op: objectstore.PermAdd, Username: "member@mgmt.test", Rights: mapi.FrightsVisible | mapi.FrightsReadAny,
	}), "Grant visible to member")
	wantNames(t, visibleTo(t, svc, "member@mgmt.test"), []string{"Shared"}, "member visible folders")

	// Provision is idempotent and preserves existing folders and grants.
	mustNoErr(t, svc.Provision("mgmt.test"), "idempotent Provision(mgmt.test)")
	all, err := svc.Folders("mgmt.test")
	mustNoErr(t, err, "Folders(mgmt.test)")
	found := false
	for _, f := range all {
		if f.ID == fid && f.DisplayName == "Shared" && len(f.Grants) >= 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("Folders(mgmt.test) did not include Shared (fid=%d) with its grants: %+v", fid, all)
	}

	// DeleteFolder removes the admin-created folder.
	mustNoErr(t, svc.DeleteFolder("mgmt.test", fid), "DeleteFolder(mgmt.test, Shared)")
	wantEq(t, len(visibleTo(t, svc, "member@mgmt.test")), 0, "visible folders after delete")
}

// checkUnprovisionedDomain proves a domain with no public store yields nothing and no
// error: the feature is simply absent rather than auto-provisioned by a read.
func checkUnprovisionedDomain(t *testing.T, svc *Service, paths fakePaths) {
	t.Helper()
	got, err := svc.VisibleFolders("nobody@unprov.test")
	mustNoErr(t, err, "VisibleFolders on an un-provisioned domain")
	wantEq(t, len(got), 0, "what an un-provisioned domain sees")
	if _, err := os.Stat(paths.HomedirFor("unprov.test")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("VisibleFolders created the un-provisioned domain's public store (stat err %v)", err)
	}
}

// seedLocalTree provisions local.test and builds the tree the way an administrator
// would: Announcements visible to anyone (and postable by poster), Staff visible only
// to poster.
func seedLocalTree(t *testing.T, svc *Service) {
	t.Helper()
	mustNoErr(t, svc.Provision("local.test"), "Provision(local.test)")
	st, err := svc.OpenForDomain("local.test")
	mustNoErr(t, err, "OpenForDomain(local.test)")
	defer mustClose(t, st)
	announce := createFolder(t, st, "Announcements")
	staff := createFolder(t, st, "Staff")
	grantAnyone(t, st, announce, mapi.FrightsVisible|mapi.FrightsReadAny)
	grantUser(t, st, announce, "poster@local.test", mapi.FrightsVisible|mapi.FrightsReadAny|mapi.FrightsCreate)
	grantUser(t, st, staff, "poster@local.test", mapi.FrightsVisible|mapi.FrightsReadAny|mapi.FrightsCreate)
}

// seedOtherTree provisions a second domain with its own anyone-visible folder.
func seedOtherTree(t *testing.T, svc *Service) {
	t.Helper()
	mustNoErr(t, svc.Provision("other.test"), "Provision(other.test)")
	st, err := svc.OpenForDomain("other.test")
	mustNoErr(t, err, "OpenForDomain(other.test)")
	defer mustClose(t, st)
	grantAnyone(t, st, createFolder(t, st, "OtherAnnounce"), mapi.FrightsVisible|mapi.FrightsReadAny)
}

// createFolder adds one public folder to a domain's store.
func createFolder(t *testing.T, st *objectstore.Store, name string) int64 {
	t.Helper()
	fid, err := st.CreateFolder(nil, name)
	mustNoErr(t, err, "CreateFolder("+name+")")
	return fid
}

// visibleTo lists the folders one caller sees.
func visibleTo(t *testing.T, svc *Service, caller string) []Folder {
	t.Helper()
	fs, err := svc.VisibleFolders(caller)
	mustNoErr(t, err, "VisibleFolders("+caller+")")
	return fs
}

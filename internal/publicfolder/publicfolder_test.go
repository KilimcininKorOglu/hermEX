package publicfolder

import (
	"path/filepath"
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
	svc := New(fakePaths{root: t.TempDir()})

	checkUnprovisionedDomain(t, svc)
	seedLocalTree(t, svc)

	// Reader holds no explicit grant: sees only the anyone-granted Announcements,
	// never Staff (which has no anyone grant).
	wantNames(t, visibleTo(t, svc, "reader@local.test"), []string{"Announcements"},
		"what a reader with no explicit grant sees")

	// Poster holds explicit grants: sees both, with post rights on Announcements.
	poster := visibleTo(t, svc, "poster@local.test")
	wantNames(t, poster, []string{"Announcements", "Staff"}, "what a poster with explicit grants sees")
	wantPostRight(t, poster, "Announcements")

	seedOtherTree(t, svc)
	// Tenant isolation: the second domain's caller sees only their domain's folder
	// and never local.test's tree, even though local.test's store exists on disk. The
	// domain is derived from the caller's address, so there is no path that opens
	// another tenant's store.
	wantNames(t, visibleTo(t, svc, "intruder@other.test"), []string{"OtherAnnounce"},
		"what an other.test caller sees")
}

// checkUnprovisionedDomain proves a domain with no public store yields nothing and no
// error: the feature is simply absent rather than auto-provisioned by a read.
func checkUnprovisionedDomain(t *testing.T, svc *Service) {
	t.Helper()
	got, err := svc.VisibleFolders("nobody@unprov.test")
	mustNoErr(t, err, "VisibleFolders on an un-provisioned domain")
	wantEq(t, len(got), 0, "what an un-provisioned domain sees")
}

// seedLocalTree provisions local.test and builds the tree the way an administrator
// would: Announcements visible to anyone (and postable by poster), Staff visible only
// to poster.
func seedLocalTree(t *testing.T, svc *Service) {
	t.Helper()
	mustNoErr(t, svc.Provision("local.test"), "Provision(local.test)")
	st, err := svc.OpenForDomain("local.test")
	mustNoErr(t, err, "OpenForDomain(local.test)")
	defer st.Close()
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
	defer st.Close()
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

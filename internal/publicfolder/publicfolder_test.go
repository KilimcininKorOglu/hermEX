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

	// A domain with no public store: nothing visible, no error — the feature is
	// simply absent rather than auto-provisioned by a read.
	if got, err := svc.VisibleFolders("nobody@unprov.test"); err != nil || got != nil {
		t.Fatalf("un-provisioned domain VisibleFolders = %v, %v; want nil, nil", got, err)
	}

	// Provision local.test and build the tree the way an administrator would:
	// Announcements visible to anyone (and postable by poster), Staff visible only
	// to poster.
	if err := svc.Provision("local.test"); err != nil {
		t.Fatal(err)
	}
	st, err := svc.OpenForDomain("local.test")
	if err != nil {
		t.Fatal(err)
	}
	announce, err := st.CreateFolder(nil, "Announcements")
	if err != nil {
		t.Fatal(err)
	}
	staff, err := st.CreateFolder(nil, "Staff")
	if err != nil {
		t.Fatal(err)
	}
	grantAnyone(t, st, announce, mapi.FrightsVisible|mapi.FrightsReadAny)
	grantUser(t, st, announce, "poster@local.test", mapi.FrightsVisible|mapi.FrightsReadAny|mapi.FrightsCreate)
	grantUser(t, st, staff, "poster@local.test", mapi.FrightsVisible|mapi.FrightsReadAny|mapi.FrightsCreate)
	st.Close()

	// Reader holds no explicit grant: sees only the anyone-granted Announcements,
	// never Staff (which has no anyone grant).
	r, err := svc.VisibleFolders("reader@local.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(r); len(got) != 1 || got[0] != "Announcements" {
		t.Errorf("reader sees %v, want [Announcements] only", got)
	}

	// Poster holds explicit grants: sees both, with post rights on Announcements.
	p, err := svc.VisibleFolders("poster@local.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(p); len(got) != 2 {
		t.Errorf("poster sees %v, want both Announcements and Staff", got)
	}
	for _, f := range p {
		if f.DisplayName == "Announcements" && f.Rights&mapi.FrightsCreate == 0 {
			t.Errorf("poster lacks the post right on Announcements: rights=%#x", f.Rights)
		}
	}

	// Tenant isolation: provision a second domain with its OWN folder, then confirm
	// a caller in that domain sees only their domain's folder and never local.test's
	// tree — even though local.test's store exists on disk. The domain is derived
	// from the caller's address, so there is no path that opens another tenant's store.
	if err := svc.Provision("other.test"); err != nil {
		t.Fatal(err)
	}
	ost, err := svc.OpenForDomain("other.test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := ost.CreateFolder(nil, "OtherAnnounce")
	if err != nil {
		t.Fatal(err)
	}
	grantAnyone(t, ost, other, mapi.FrightsVisible|mapi.FrightsReadAny)
	ost.Close()

	o, err := svc.VisibleFolders("intruder@other.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(o); len(got) != 1 || got[0] != "OtherAnnounce" {
		t.Errorf("cross-tenant routing leak: other.test caller sees %v, want [OtherAnnounce] only", got)
	}
}

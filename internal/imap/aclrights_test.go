package imap

import (
	"path/filepath"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestACLRightsFollowRFC4314 reads SETACL rights strings by RFC 4314. An
// unrecognized right "MUST cause the command to return the BAD response" and must
// not be silently ignored (3.1), so a typo cannot grant less than the client asked
// for under an OK. The obsolete "d" and "c" rights are virtual (2.1.1): "d" stands
// for every member of the delete right, which the RFC example expands to "et",
// and "c" for the create right, the union of "k" and "x".
func TestACLRightsFollowRFC4314(t *testing.T) {
	for _, bad := range []string{"lrz", "lr!", "lr0", "lr ", "R"} {
		if _, ok := aclToRights(bad); ok {
			t.Errorf("rights %q were accepted, want BAD for the unrecognized right", bad)
		}
	}
	d, ok := aclToRights("d")
	et, _ := aclToRights("et")
	if !ok || d != et {
		t.Errorf(`"d" = %#x (ok %v), want the rights of "et" %#x`, d, ok, et)
	}
	c, ok := aclToRights("c")
	kx, _ := aclToRights("kx")
	if !ok || c != kx {
		t.Errorf(`"c" = %#x (ok %v), want the rights of "kx" %#x`, c, ok, kx)
	}
	if r, ok := aclToRights("lrswipkxtea"); !ok || r == 0 {
		t.Errorf("the standard rights were refused (%#x, %v)", r, ok)
	}
	if r, ok := aclToRights(""); !ok || r != 0 {
		t.Errorf("an empty rights string = %#x, %v; want no rights, accepted", r, ok)
	}
}

// TestSetACLAdjustmentNeedsTheStoredRights proves a "+" or "-" SETACL computes
// from the identifier's stored rights, and fails when they cannot be read: taking
// a failed read as no rights would let "+r" replace an editor's rights with r.
func TestSetACLAdjustmentNeedsTheStoredRights(t *testing.T) {
	st, err := objectstore.Open(filepath.Join(t.TempDir(), "mbox"))
	mustNoErr(t, "open the store", err)
	fid := int64(mapi.PrivateFIDInbox)
	grantUser(t, st, fid, "bob@local.test", mapi.RightsEditor)

	got, err := adjustedRights(st, fid, "bob@local.test", '-', mapi.FrightsDeleteAny)
	mustNoErr(t, "adjust readable rights", err)
	if got != mapi.RightsEditor&^mapi.FrightsDeleteAny {
		t.Errorf("-t on an editor = %#x, want %#x", got, mapi.RightsEditor&^mapi.FrightsDeleteAny)
	}
	if got, _ := adjustedRights(st, fid, "bob@local.test", 0, mapi.FrightsReadAny); got != mapi.FrightsReadAny {
		t.Errorf("a plain rights string = %#x, want it to replace the rights", got)
	}

	st.Close()
	if got, err := adjustedRights(st, fid, "bob@local.test", '+', mapi.FrightsReadAny); err == nil {
		t.Errorf("+r with an unreadable access list = %#x, want an error", got)
	}
	if _, err := aclMemberID(st, fid, "bob@local.test"); err == nil {
		t.Error("the member id was read from an unreadable access list")
	}
}

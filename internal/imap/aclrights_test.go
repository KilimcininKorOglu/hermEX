package imap

import (
	"path/filepath"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

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

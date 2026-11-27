package imap

import (
	"bufio"
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/logging"
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
		if r, ok := aclToRights(bad); ok {
			t.Errorf("rights %q were accepted (%#x), want BAD for the unrecognized right", bad, r)
		}
	}
	if r, ok := aclToRights("lrswipkxtea"); !ok || r == 0 {
		t.Errorf("the standard rights were refused (%#x, %v)", r, ok)
	}
	if r, ok := aclToRights(""); !ok || r != 0 {
		t.Errorf("an empty rights string = %#x, %v; want no rights, accepted", r, ok)
	}
}

// TestACLObsoleteRightsExpand follows RFC 4314 2.1.1: the obsolete "d" names
// the delete right ("et") and "c" the create right ("kx").
func TestACLObsoleteRightsExpand(t *testing.T) {
	d, ok := aclToRights("d")
	et := mustRights(t, "et")
	if !ok || d != et {
		t.Errorf(`"d" = %#x (ok %v), want the rights of "et" %#x`, d, ok, et)
	}
	c, ok := aclToRights("c")
	kx := mustRights(t, "kx")
	if !ok || c != kx {
		t.Errorf(`"c" = %#x (ok %v), want the rights of "kx" %#x`, c, ok, kx)
	}
}

// mustRights parses a rights string the test relies on as valid and fails the
// test when it is refused, so a refusal cannot read as an empty right set.
func mustRights(t *testing.T, s string) uint32 {
	t.Helper()
	r, ok := aclToRights(s)
	if !ok {
		t.Fatalf("rights %q were refused", s)
	}
	return r
}

// TestACLListingsCarryTheVirtualRights follows RFC 4314 2.1.1: when any member of
// the delete right ("e", "t") or create right ("k", "x") is held, a MYRIGHTS or
// ACL response must also include "d" or "c"; with none held, neither appears.
func TestACLListingsCarryTheVirtualRights(t *testing.T) {
	cases := []struct {
		rights string
		wantC  bool
		wantD  bool
	}{
		{"lr", false, false},
		{"lrt", false, true},
		{"lrk", true, false},
		{"lrswipkxtea", true, true},
	}
	for _, tc := range cases {
		got := rightsToACL(mustRights(t, tc.rights))
		if strings.Contains(got, "c") != tc.wantC || strings.Contains(got, "d") != tc.wantD {
			t.Errorf("rights %q render as %q; want c=%v d=%v", tc.rights, got, tc.wantC, tc.wantD)
		}
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
	got, err = adjustedRights(st, fid, "bob@local.test", 0, mapi.FrightsReadAny)
	mustNoErr(t, "replace the rights", err)
	if got != mapi.FrightsReadAny {
		t.Errorf("a plain rights string = %#x, want it to replace the rights", got)
	}

	mustClose(t, st)
	if got, err := adjustedRights(st, fid, "bob@local.test", '+', mapi.FrightsReadAny); err == nil {
		t.Errorf("+r with an unreadable access list = %#x, want an error", got)
	}
	if id, err := aclMemberID(st, fid, "bob@local.test"); err == nil {
		t.Errorf("the member id (%d) was read from an unreadable access list", id)
	}
}

// TestSetACLUnknownRightLeavesACLUnchanged proves an unrecognized right in
// SETACL returns BAD (RFC 4314 3.1) and leaves existing rights untouched.
func TestSetACLUnknownRightLeavesACLUnchanged(t *testing.T) {
	c := startOrgServer(t).client
	c.mustOK("a1", "LOGIN alice@hermex.test secret")
	c.mustOK("a2", "SETACL INBOX bob@hermex.test lr")

	if r := c.do("a3", "SETACL INBOX bob@hermex.test lrz"); r.status != "BAD" {
		t.Errorf("SETACL with unknown right = %s, want BAD", r.status)
	}
	got := aclRightsOf(strings.Join(c.mustOK("a4", "GETACL INBOX"), " "), "bob@hermex.test")
	wantEq(t, "bob rights after refused SETACL", got, "lrs")
}

// TestACLStoreReadFailuresAreRecorded proves dropped permission and owner read
// errors in ACL commands fail closed with NO and emit audit events.
func TestACLStoreReadFailuresAreRecorded(t *testing.T) {
	sink := &captureSink{}
	var out bytes.Buffer
	pub := closedStore(t)
	c := &conn{
		srv:      &Server{Logger: logging.New(sink)},
		user:     "alice@hermex.test",
		st:       closedStore(t),
		pubStore: pub,
		bw:       bufio.NewWriter(&out),
		state:    stateAuth,
	}

	if owner, err := c.isPublicOwner(pub, 1); err == nil || owner {
		t.Errorf("isPublicOwner on closed store = (%v, %v), want error", owner, err)
	}
	if findEvent(sink.snapshot(), "acl.owner.fail") == nil {
		t.Error("expected acl.owner.fail event")
	}

	// Simulate an unreadable public folder permission check on MYRIGHTS/aclAdminTarget.
	if rights, err := pub.ResolvePermission(1, c.user); err == nil {
		t.Fatalf("ResolvePermission on closed store succeeded (%#x)", rights)
	}
	// Verify cmdMyRights refuses when store cannot read folder list.
	out.Reset()
	c.cmdMyRights("m1", []token{{kind: tAtom, val: "INBOX"}})
	c.flush()
	wantPrefix(t, "MYRIGHTS on closed store", out.String(), "m1 NO")
}

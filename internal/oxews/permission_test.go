package oxews

import (
	"testing"

	"hermex/internal/mapi"
)

func deref(p *bool) bool { return p != nil && *p }

// storedRights runs a wire permission through the write path exactly as the EWS
// handler does: compute the raw mask, restrict to the client-sendable set, and
// normalize as the store records it.
func storedRights(p Permission) uint32 {
	return mapi.NormalizeRights(PermissionRights(p)&mapi.RightsMaxROP, true)
}

// TestPermissionLevelRoundTrip is the mapping's authoritative check: each canned
// PermissionLevel, sent on the wire, must survive the write path (mask + normalize
// as the store does) and be recovered as the same level on read. This proves the
// level table is consistent in both directions — including Owner, which the store
// normalizes (owner implies visible|contact) and a raw-mask compare would miss.
func TestPermissionLevelRoundTrip(t *testing.T) {
	levels := []string{
		"None", "Owner", "PublishingEditor", "Editor", "PublishingAuthor",
		"Author", "NoneditingAuthor", "Reviewer", "Contributor",
	}
	for _, level := range levels {
		stored := storedRights(Permission{PermissionLevel: level})
		if got := levelForRights(stored); got != level {
			t.Errorf("level %q did not round-trip: stored=%#x, read back as %q", level, stored, got)
		}
	}
}

// TestPermissionLevelFreeBusyAgnostic confirms the level lookup tolerates the
// free/busy fill: a member with raw RightsNone (as the read path synthesizes for an
// absent Default/Anonymous) reads back as None, and a free/busy-filled Reviewer (as
// the store records it) still reads as Reviewer.
func TestPermissionLevelFreeBusyAgnostic(t *testing.T) {
	if got := levelForRights(mapi.RightsNone); got != "None" {
		t.Errorf("raw RightsNone → %q, want None", got)
	}
	if got := levelForRights(mapi.NormalizeRights(mapi.RightsReviewer, true)); got != "Reviewer" {
		t.Errorf("free/busy-filled Reviewer → %q, want Reviewer", got)
	}
}

// TestPermissionEditDeleteAllRoundTrip confirms EditItems/DeleteItems="All" survive
// the round-trip. The reference's write loses the Owned bit on "All"; the store's
// normalize fills it (editAny implies editOwned), so "All" reads back as "All" —
// more correct than the reference, and the reason a round-trip is the right test.
func TestPermissionEditDeleteAllRoundTrip(t *testing.T) {
	stored := storedRights(Permission{
		PermissionLevel: "Custom",
		EditItems:       "All",
		DeleteItems:     "All",
		ReadItems:       "FullDetails",
	})
	back := PermissionFromRights(mapi.MemberIDDefault, "", stored)
	if back.EditItems != "All" {
		t.Errorf("EditItems=All did not round-trip: got %q", back.EditItems)
	}
	if back.DeleteItems != "All" {
		t.Errorf("DeleteItems=All did not round-trip: got %q", back.DeleteItems)
	}
}

// TestPermissionFlagsRoundTrip confirms the individual flags an Editor implies are
// recovered on read, and that a right the level does NOT grant (create-subfolders)
// stays off.
func TestPermissionFlagsRoundTrip(t *testing.T) {
	back := PermissionFromRights(mapi.MemberIDDefault, "", storedRights(Permission{PermissionLevel: "Editor"}))
	if back.PermissionLevel != "Editor" {
		t.Fatalf("level = %q, want Editor", back.PermissionLevel)
	}
	if back.ReadItems != "FullDetails" || !deref(back.CanCreateItems) || back.EditItems != "All" || back.DeleteItems != "All" {
		t.Errorf("Editor flags wrong: read=%q create=%v edit=%q delete=%q", back.ReadItems, deref(back.CanCreateItems), back.EditItems, back.DeleteItems)
	}
	if deref(back.CanCreateSubFolders) {
		t.Error("Editor must not grant CanCreateSubFolders")
	}
}

// TestPermissionCustomLevel confirms a rights combination that is not a canned
// level reports as Custom (Reviewer rights plus create-subfolders is no role).
func TestPermissionCustomLevel(t *testing.T) {
	stored := storedRights(Permission{PermissionLevel: "Reviewer", CanCreateSubFolders: new(true)})
	if got := levelForRights(stored); got != "Custom" {
		t.Errorf("Reviewer + create-subfolders should be Custom, got %q", got)
	}
}

// TestPermissionUserIDMapping confirms the member id maps to the right UserId form:
// the special Default/Anonymous members to DistinguishedUser, a real member to its
// SMTP address.
func TestPermissionUserIDMapping(t *testing.T) {
	if u := PermissionFromRights(mapi.MemberIDDefault, "default", 0).UserID; u.DistinguishedUser != "Default" {
		t.Errorf("member 0 → %+v, want DistinguishedUser=Default", u)
	}
	if u := PermissionFromRights(mapi.MemberIDAnonymous, "anonymous", 0).UserID; u.DistinguishedUser != "Anonymous" {
		t.Errorf("member -1 → %+v, want DistinguishedUser=Anonymous", u)
	}
	u := PermissionFromRights(5, "bob@hermex.test", mapi.RightsReviewer).UserID
	if u.PrimarySmtpAddress != "bob@hermex.test" || u.DistinguishedUser != "" {
		t.Errorf("real member → %+v, want PrimarySmtpAddress=bob@hermex.test", u)
	}
}

package objectstore

import (
	"testing"

	"hermex/internal/mapi"
)

// TestStoreOwnersRoundTrip proves the store-owner list persists, reads back empty on a
// fresh mailbox, and clears.
func TestStoreOwnersRoundTrip(t *testing.T) {
	s := openTestStore(t)

	if got, err := s.GetStoreOwners(); err != nil || len(got) != 0 {
		t.Fatalf("fresh store owners = %v, %v; want empty", got, err)
	}
	want := []string{"boss@hermex.test", "assistant@hermex.test"}
	if err := s.SetStoreOwners(want); err != nil {
		t.Fatalf("SetStoreOwners: %v", err)
	}
	got, err := s.GetStoreOwners()
	if err != nil {
		t.Fatalf("GetStoreOwners: %v", err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("store owners = %v, want %v", got, want)
	}
	if err := s.SetStoreOwners(nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, _ := s.GetStoreOwners(); len(got) != 0 {
		t.Errorf("after clear = %v, want empty", got)
	}
}

// TestIsStoreOwnerCaseFold proves ownership is matched case-insensitively (the same
// identity key the delegate-list and folder-permission checks use), so a grant is
// honored regardless of the caller's address casing, and a non-owner is rejected.
func TestIsStoreOwnerCaseFold(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetStoreOwners([]string{"Boss@Hermex.test"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.IsStoreOwner("boss@hermex.TEST"); err != nil || !ok {
		t.Errorf("IsStoreOwner(case-variant) = %v, %v; want true", ok, err)
	}
	if ok, _ := s.IsStoreOwner("stranger@hermex.test"); ok {
		t.Error("IsStoreOwner(non-owner) = true; want false")
	}
}

// TestResolvePermissionStoreOwnerElevates proves the permission resolver grants a store
// owner full member rights on a folder where they hold no explicit grant, while a
// non-owner on the same folder resolves to nothing. This is the single chokepoint every
// access check goes through, so the elevation reaches all of them.
func TestResolvePermissionStoreOwnerElevates(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetStoreOwners([]string{"boss@hermex.test"}); err != nil {
		t.Fatal(err)
	}

	owner, err := s.ResolvePermission(int64(mapi.PrivateFIDInbox), "boss@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if owner != mapi.RightsMaxROP {
		t.Errorf("store-owner rights on Inbox = 0x%04X, want 0x%04X (RightsMaxROP)", owner, mapi.RightsMaxROP)
	}

	other, err := s.ResolvePermission(int64(mapi.PrivateFIDInbox), "stranger@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if other != 0 {
		t.Errorf("non-owner rights on an ungranted Inbox = 0x%04X, want 0", other)
	}
}

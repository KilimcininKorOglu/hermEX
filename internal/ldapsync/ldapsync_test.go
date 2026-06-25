package ldapsync

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/ldapauth"
)

type fakeSyncer struct {
	users  []ldapauth.SyncedUser
	groups []ldapauth.SyncedGroup
}

func (f *fakeSyncer) Sync(directory.LDAPConfig) ([]ldapauth.SyncedUser, error) { return f.users, nil }
func (f *fakeSyncer) SyncGroups(directory.LDAPConfig) ([]ldapauth.SyncedGroup, error) {
	return f.groups, nil
}

type fakeStore struct {
	profiles     map[string]map[string]string
	groupOwner   map[string]string
	groupMembers map[string][]string
	mastered     []directory.MListInfo
	deleted      []string
}

func (f *fakeStore) UpsertLDAPUser(string, []byte, string) (bool, error) { return true, nil }
func (f *fakeStore) ApplyLDAPProfile(u string, v map[string]string) (bool, error) {
	if f.profiles == nil {
		f.profiles = map[string]map[string]string{}
	}
	f.profiles[u] = v
	return true, nil
}
func (f *fakeStore) UpsertLDAPGroup(list string, _ []byte, owner string, members []string) (bool, error) {
	if f.groupOwner == nil {
		f.groupOwner, f.groupMembers = map[string]string{}, map[string][]string{}
	}
	f.groupOwner[list], f.groupMembers[list] = owner, members
	return true, nil
}
func (f *fakeStore) ListMLists() ([]directory.MListInfo, error) { return f.mastered, nil }
func (f *fakeStore) DeleteMList(list string) (bool, error) {
	f.deleted = append(f.deleted, list)
	return true, nil
}

// TestRunUsersAndGroups proves Run applies a user's profile fields, resolves a
// group's owner and members from the synced users' DNs (skipping an unresolved DN),
// and prunes a mastered list the directory no longer has while keeping a local one.
func TestRunUsersAndGroups(t *testing.T) {
	syncer := &fakeSyncer{
		users: []ldapauth.SyncedUser{
			{Username: "alice@hermex.test", DN: "uid=alice,dc=x", Fields: map[string]string{"title": "Eng"}},
			{Username: "bob@hermex.test", DN: "uid=bob,dc=x"},
		},
		groups: []ldapauth.SyncedGroup{
			{Mail: "eng@hermex.test", OwnerDN: "uid=alice,dc=x",
				MemberDNs: []string{"uid=alice,dc=x", "uid=bob,dc=x", "uid=ghost,dc=x"}},
		},
	}
	store := &fakeStore{mastered: []directory.MListInfo{
		{Listname: "eng@hermex.test", LDAPMastered: true},
		{Listname: "old@hermex.test", LDAPMastered: true},    // gone from the directory -> pruned
		{Listname: "local@hermex.test", LDAPMastered: false}, // locally managed -> kept
	}}

	if _, err := Run(directory.LDAPConfig{SyncGroups: true}, syncer, store,
		func(string) string { return "" }, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}

	if store.profiles["alice@hermex.test"]["title"] != "Eng" {
		t.Errorf("alice profile = %v, want title=Eng", store.profiles["alice@hermex.test"])
	}
	if store.groupOwner["eng@hermex.test"] != "alice@hermex.test" || len(store.groupMembers["eng@hermex.test"]) != 2 {
		t.Errorf("eng group owner=%q members=%v, want alice + 2 (ghost skipped)",
			store.groupOwner["eng@hermex.test"], store.groupMembers["eng@hermex.test"])
	}
	if len(store.deleted) != 1 || store.deleted[0] != "old@hermex.test" {
		t.Errorf("pruned = %v, want [old@hermex.test]", store.deleted)
	}
}

package ldapsync

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/ldapauth"
)

type fakeSyncer struct {
	users    []ldapauth.SyncedUser
	groups   []ldapauth.SyncedGroup
	contacts []ldapauth.SyncedContact
}

func (f *fakeSyncer) Sync(directory.LDAPConfig) ([]ldapauth.SyncedUser, error) { return f.users, nil }
func (f *fakeSyncer) SyncGroups(directory.LDAPConfig) ([]ldapauth.SyncedGroup, error) {
	return f.groups, nil
}
func (f *fakeSyncer) SyncContacts(directory.LDAPConfig) ([]ldapauth.SyncedContact, error) {
	return f.contacts, nil
}

type fakeStore struct {
	profiles        map[string]map[string]string
	groupOwner      map[string]string
	groupMembers    map[string][]string
	mastered        []directory.MListInfo
	deleted         []string
	contacts        []directory.ContactInfo
	upsertedNames   map[string]string
	contactDomain   string
	deletedContacts []string
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
func (f *fakeStore) UpsertLDAPContact(email string, _ []byte, displayName, domain string) (bool, error) {
	if f.upsertedNames == nil {
		f.upsertedNames = map[string]string{}
	}
	f.upsertedNames[email] = displayName
	f.contactDomain = domain
	return true, nil
}
func (f *fakeStore) ListContacts() ([]directory.ContactInfo, error) { return f.contacts, nil }
func (f *fakeStore) DeleteLDAPContact(email string) (bool, error) {
	f.deletedContacts = append(f.deletedContacts, email)
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

// contactRun performs a contact-only downsync over the given syncer and store.
func contactRun(t *testing.T, syncer *fakeSyncer, store *fakeStore) {
	t.Helper()
	cfg := directory.LDAPConfig{SyncContacts: true, ContactDomain: "hermex.test"}
	if _, err := Run(cfg, syncer, store,
		func(string) string { return "" }, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
}

// TestRunSyncsContacts proves a directory contact reaches the local address book with its
// display name.
func TestRunSyncsContacts(t *testing.T) {
	syncer := &fakeSyncer{contacts: []ldapauth.SyncedContact{
		{Mail: "partner@remote.test", DisplayName: "Partner Inc", ExternID: []byte{1, 2}},
	}}
	store := &fakeStore{}

	contactRun(t, syncer, store)

	if store.upsertedNames["partner@remote.test"] != "Partner Inc" {
		t.Errorf("upserted = %v, want partner@remote.test with its display name", store.upsertedNames)
	}
}

// TestRunFilesContactsUnderTheConfiguredDomain proves the filing domain reaches the store:
// a contact's own address is external, so it cannot supply one.
func TestRunFilesContactsUnderTheConfiguredDomain(t *testing.T) {
	syncer := &fakeSyncer{contacts: []ldapauth.SyncedContact{
		{Mail: "partner@remote.test", DisplayName: "Partner Inc", ExternID: []byte{1}},
	}}
	store := &fakeStore{}

	contactRun(t, syncer, store)

	if store.contactDomain != "hermex.test" {
		t.Errorf("filing domain = %q, want hermex.test", store.contactDomain)
	}
}

// TestRunRefusesContactSyncWithoutADomain proves an enabled contact sync with no filing
// domain is an error rather than a silent no-op, because a contact cannot be filed at all
// without one.
func TestRunRefusesContactSyncWithoutADomain(t *testing.T) {
	_, err := Run(directory.LDAPConfig{SyncContacts: true}, &fakeSyncer{}, &fakeStore{},
		func(string) string { return "" }, func(string, ...any) {})

	if err == nil {
		t.Error("contact sync with no filing domain must be an error")
	}
}

// TestRunPrunesAContactGoneFromLDAP is the load-bearing prune case: a contact the directory
// no longer publishes must not linger in the GAL.
func TestRunPrunesAContactGoneFromLDAP(t *testing.T) {
	store := &fakeStore{contacts: []directory.ContactInfo{
		{Address: "old@remote.test", LDAPID: "aabb"},
	}}

	contactRun(t, &fakeSyncer{}, store)

	if len(store.deletedContacts) != 1 || store.deletedContacts[0] != "old@remote.test" {
		t.Errorf("pruned = %v, want [old@remote.test]", store.deletedContacts)
	}
}

// TestRunKeepsAHandMadeContact proves the prune pass never touches a contact an operator
// created, which carries no LDAP id.
func TestRunKeepsAHandMadeContact(t *testing.T) {
	store := &fakeStore{contacts: []directory.ContactInfo{
		{Address: "local@remote.test"},
	}}

	contactRun(t, &fakeSyncer{}, store)

	if len(store.deletedContacts) != 0 {
		t.Errorf("pruned = %v, want nothing: a contact with no LDAP id is locally managed", store.deletedContacts)
	}
}

// TestRunSkipsContactsWhenDisabled proves the pass does not run, and prunes nothing, while
// contact sync is off.
func TestRunSkipsContactsWhenDisabled(t *testing.T) {
	store := &fakeStore{contacts: []directory.ContactInfo{{Address: "old@remote.test", LDAPID: "aabb"}}}

	if _, err := Run(directory.LDAPConfig{}, &fakeSyncer{}, store,
		func(string) string { return "" }, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}

	if len(store.deletedContacts) != 0 {
		t.Errorf("pruned = %v, want nothing while contact sync is off", store.deletedContacts)
	}
}

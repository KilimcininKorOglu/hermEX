package ldapsync

import (
	"errors"
	"slices"
	"strings"
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

// fakeStore keeps lists and contacts per domain id, so a test can prove a pass
// reads and prunes only the bound domain's.
type fakeStore struct {
	upsertedUsers   []string
	profiles        map[string]map[string]string
	aliases         map[string][]string
	groupOwner      map[string]string
	groupMembers    map[string][]string
	lists           map[int64][]directory.MListInfo
	deleted         []string
	contacts        map[int64][]directory.ContactInfo
	upsertedNames   map[string]string
	contactDomain   string
	deletedContacts []string
}

func (f *fakeStore) UpsertLDAPUser(u string, _ []byte, _ string) (bool, error) {
	f.upsertedUsers = append(f.upsertedUsers, u)
	return true, nil
}
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
func (f *fakeStore) SyncAliasesFor(username string, aliases []string) ([]string, bool, error) {
	if f.aliases == nil {
		f.aliases = map[string][]string{}
	}
	f.aliases[username] = aliases
	return nil, true, nil
}
func (f *fakeStore) ListMListsInDomain(id int64) ([]directory.MListInfo, error) {
	return f.lists[id], nil
}
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
func (f *fakeStore) ListContactsInDomain(id int64) ([]directory.ContactInfo, error) {
	return f.contacts[id], nil
}
func (f *fakeStore) DeleteLDAPContact(email string) (bool, error) {
	f.deletedContacts = append(f.deletedContacts, email)
	return true, nil
}

// bound returns a configuration bound to hermex.test (domain id 1).
func bound(cfg directory.LDAPConfig) directory.LDAPConfig {
	cfg.DomainID, cfg.Domain = 1, "hermex.test"
	return cfg
}

// mustRun runs a downsync and requires it to succeed.
func mustRun(t *testing.T, cfg directory.LDAPConfig, syncer *fakeSyncer, store *fakeStore) string {
	t.Helper()
	summary, err := Run(cfg, syncer, store, func(string) string { return "" }, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	return summary
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
	store := &fakeStore{lists: map[int64][]directory.MListInfo{1: {
		{Listname: "eng@hermex.test", LDAPMastered: true},
		{Listname: "old@hermex.test", LDAPMastered: true},    // gone from the directory -> pruned
		{Listname: "local@hermex.test", LDAPMastered: false}, // locally managed -> kept
	}}}

	mustRun(t, bound(directory.LDAPConfig{SyncGroups: true}), syncer, store)

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

// TestRunRefusesUnboundConfig proves a configuration with no bound domain is refused
// before anything is read or written: it could scope neither creation nor pruning.
func TestRunRefusesUnboundConfig(t *testing.T) {
	store := &fakeStore{}
	_, err := Run(directory.LDAPConfig{}, &fakeSyncer{users: []ldapauth.SyncedUser{{Username: "a@hermex.test"}}},
		store, func(string) string { return "" }, func(string, ...any) {})
	if !errors.Is(err, ErrNoDomain) {
		t.Errorf("Run(unbound) = %v, want ErrNoDomain", err)
	}
	if len(store.upsertedUsers) != 0 {
		t.Errorf("an unbound run upserted %v", store.upsertedUsers)
	}
}

// TestRunSkipsForeignUsersButKeepsThemAsMembers proves an account in another domain
// is neither created nor updated, is counted in the summary, and still resolves as
// a member of a group in the bound domain.
func TestRunSkipsForeignUsersButKeepsThemAsMembers(t *testing.T) {
	syncer := &fakeSyncer{
		users: []ldapauth.SyncedUser{
			{Username: "alice@hermex.test", DN: "uid=alice,dc=x"},
			{Username: "carol@other.test", DN: "uid=carol,dc=x"},
		},
		groups: []ldapauth.SyncedGroup{
			{Mail: "eng@hermex.test", MemberDNs: []string{"uid=alice,dc=x", "uid=carol,dc=x"}},
			{Mail: "ops@other.test", MemberDNs: []string{"uid=carol,dc=x"}},
		},
	}
	store := &fakeStore{}

	summary := mustRun(t, bound(directory.LDAPConfig{SyncGroups: true}), syncer, store)

	if !slices.Equal(store.upsertedUsers, []string{"alice@hermex.test"}) {
		t.Errorf("upserted users = %v, want only alice@hermex.test", store.upsertedUsers)
	}
	if got := store.groupMembers["eng@hermex.test"]; !slices.Contains(got, "carol@other.test") {
		t.Errorf("eng members = %v, want the foreign member kept as a reference", got)
	}
	if _, synced := store.groupOwner["ops@other.test"]; synced {
		t.Error("a group of another domain was synced")
	}
	if !strings.Contains(summary, "1 outside hermex.test") {
		t.Errorf("summary %q does not count the skipped entries", summary)
	}
}

// TestTwoBindingsDoNotPruneEachOther proves the prune pass of one binding reads and
// deletes only its own domain's lists and contacts: two domains bound to one
// directory would otherwise delete each other's synced entries on every run.
func TestTwoBindingsDoNotPruneEachOther(t *testing.T) {
	store := &fakeStore{
		lists: map[int64][]directory.MListInfo{
			1: {{Listname: "gone@hermex.test", LDAPMastered: true}},
			2: {{Listname: "eng@other.test", LDAPMastered: true}},
		},
		contacts: map[int64][]directory.ContactInfo{
			1: {{Address: "gone@remote.test", LDAPID: "aa"}},
			2: {{Address: "partner@remote.test", LDAPID: "bb"}},
		},
	}
	cfg := bound(directory.LDAPConfig{SyncGroups: true, SyncContacts: true})

	mustRun(t, cfg, &fakeSyncer{}, store)

	if !slices.Equal(store.deleted, []string{"gone@hermex.test"}) {
		t.Errorf("pruned lists = %v, want only the bound domain's", store.deleted)
	}
	if !slices.Equal(store.deletedContacts, []string{"gone@remote.test"}) {
		t.Errorf("pruned contacts = %v, want only the bound domain's", store.deletedContacts)
	}
}

// TestRunAppliesAliases proves the addresses the directory publishes reach the account once
// an alias attribute is configured, including an address in another local domain.
func TestRunAppliesAliases(t *testing.T) {
	syncer := &fakeSyncer{users: []ldapauth.SyncedUser{
		{Username: "alice@hermex.test", Aliases: []string{"sales@hermex.test", "alice@other.test"}},
	}}
	store := &fakeStore{}

	mustRun(t, bound(directory.LDAPConfig{AliasAttr: "proxyAddresses"}), syncer, store)

	want := []string{"sales@hermex.test", "alice@other.test"}
	if got := store.aliases["alice@hermex.test"]; !slices.Equal(got, want) {
		t.Errorf("applied aliases = %v, want %v", got, want)
	}
}

// TestRunLeavesAliasesAloneWhenUnconfigured is the load-bearing guard: a downsync with no
// alias attribute must not touch the alias set, or it would delete every alias an operator
// added by hand.
func TestRunLeavesAliasesAloneWhenUnconfigured(t *testing.T) {
	syncer := &fakeSyncer{users: []ldapauth.SyncedUser{{Username: "alice@hermex.test"}}}
	store := &fakeStore{}

	mustRun(t, bound(directory.LDAPConfig{}), syncer, store)

	if _, touched := store.aliases["alice@hermex.test"]; touched {
		t.Error("the alias set was replaced while no alias attribute is configured")
	}
}

// contactRun performs a contact-only downsync over the given syncer and store.
func contactRun(t *testing.T, syncer *fakeSyncer, store *fakeStore) {
	t.Helper()
	mustRun(t, bound(directory.LDAPConfig{SyncContacts: true}), syncer, store)
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

// TestRunFilesContactsUnderTheBoundDomain proves the bound domain reaches the store as
// the filing domain: a contact's own address is external, so it cannot supply one.
func TestRunFilesContactsUnderTheBoundDomain(t *testing.T) {
	syncer := &fakeSyncer{contacts: []ldapauth.SyncedContact{
		{Mail: "partner@remote.test", DisplayName: "Partner Inc", ExternID: []byte{1}},
	}}
	store := &fakeStore{}

	contactRun(t, syncer, store)

	if store.contactDomain != "hermex.test" {
		t.Errorf("filing domain = %q, want hermex.test", store.contactDomain)
	}
}

// TestRunPrunesAContactGoneFromLDAP is the load-bearing prune case: a contact the directory
// no longer publishes must not linger in the GAL.
func TestRunPrunesAContactGoneFromLDAP(t *testing.T) {
	store := &fakeStore{contacts: map[int64][]directory.ContactInfo{1: {
		{Address: "old@remote.test", LDAPID: "aabb"},
	}}}

	contactRun(t, &fakeSyncer{}, store)

	if len(store.deletedContacts) != 1 || store.deletedContacts[0] != "old@remote.test" {
		t.Errorf("pruned = %v, want [old@remote.test]", store.deletedContacts)
	}
}

// TestRunKeepsAHandMadeContact proves the prune pass never touches a contact an operator
// created, which carries no LDAP id.
func TestRunKeepsAHandMadeContact(t *testing.T) {
	store := &fakeStore{contacts: map[int64][]directory.ContactInfo{1: {
		{Address: "local@remote.test"},
	}}}

	contactRun(t, &fakeSyncer{}, store)

	if len(store.deletedContacts) != 0 {
		t.Errorf("pruned = %v, want nothing: a contact with no LDAP id is locally managed", store.deletedContacts)
	}
}

// TestRunSkipsContactsWhenDisabled proves the pass does not run, and prunes nothing, while
// contact sync is off.
func TestRunSkipsContactsWhenDisabled(t *testing.T) {
	store := &fakeStore{contacts: map[int64][]directory.ContactInfo{1: {{Address: "old@remote.test", LDAPID: "aabb"}}}}

	mustRun(t, bound(directory.LDAPConfig{}), &fakeSyncer{}, store)

	if len(store.deletedContacts) != 0 {
		t.Errorf("pruned = %v, want nothing while contact sync is off", store.deletedContacts)
	}
}

// fakeBindings resolves binding ids to configurations.
type fakeBindings map[int64]directory.LDAPConfig

func (f fakeBindings) LDAPConfigForBinding(id int64) (directory.LDAPConfig, bool, error) {
	cfg, ok := f[id]
	return cfg, ok, nil
}

// TestRunBindingsRunsEveryBinding proves each binding is synced into its own domain,
// and an unknown binding is reported without stopping the others.
func TestRunBindingsRunsEveryBinding(t *testing.T) {
	src := fakeBindings{
		1: {DomainID: 1, Domain: "a.test"},
		2: {DomainID: 2, Domain: "b.test"},
	}
	syncer := &fakeSyncer{users: []ldapauth.SyncedUser{{Username: "u@a.test"}, {Username: "u@b.test"}}}
	store := &fakeStore{}

	summary, err := RunBindings([]int64{1, 9, 2}, src, syncer, store,
		func(string) string { return "" }, func(string, ...any) {})

	if err == nil || !strings.Contains(err.Error(), "binding 9") {
		t.Errorf("RunBindings error = %v, want the unknown binding reported", err)
	}
	if !slices.Equal(store.upsertedUsers, []string{"u@a.test", "u@b.test"}) {
		t.Errorf("upserted = %v, want one user per bound domain", store.upsertedUsers)
	}
	if !strings.Contains(summary, "a.test: ") || !strings.Contains(summary, "b.test: ") {
		t.Errorf("summary %q does not name both domains", summary)
	}
}

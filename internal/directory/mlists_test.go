package directory

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

// mlistTestDir builds a directory with two domains and a handful of mailbox users
// for exercising distribution-list expansion. Returns the directory ready for
// CreateMList calls.
func mlistTestDir(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	for _, dom := range []string{"hermex.test", "partner.test"} {
		if _, err := d.CreateDomain(dom, filepath.Join(root, "domains", dom)); err != nil {
			t.Fatalf("create domain %s: %v", dom, err)
		}
	}
	for _, u := range []string{"alice@hermex.test", "bob@hermex.test", "carol@hermex.test", "dave@partner.test"} {
		if _, err := d.CreateUser(u, "pw", filepath.Join(root, "users", u)); err != nil {
			t.Fatalf("create user %s: %v", u, err)
		}
	}
	return d
}

// mkList creates a list of the given type and privilege with the given members.
func mkList(t *testing.T, d *SQLDirectory, addr string, listType, listPriv int, members ...string) {
	t.Helper()
	if _, err := d.CreateMList(addr, listType, listPriv); err != nil {
		t.Fatalf("create list %s: %v", addr, err)
	}
	if len(members) > 0 {
		if _, err := d.SetMembers(addr, members); err != nil {
			t.Fatalf("set members of %s: %v", addr, err)
		}
	}
}

func expandSorted(t *testing.T, d *SQLDirectory, list, from string) ([]string, MListResult) {
	t.Helper()
	got, res, err := d.ExpandMList(list, from)
	if err != nil {
		t.Fatalf("ExpandMList(%s, %s): %v", list, from, err)
	}
	slices.Sort(got)
	return got, res
}

// TestExpandMListPrivilege pins the posting-privilege gate for every mlist_priv
// against a normal-type list: the gate decides who may post, and a refusal
// returns the matching result code and NO members (so the MTA can bounce without
// fanning out). The membership is the same so only the privilege varies.
func TestExpandMListPrivilege(t *testing.T) {
	d := mlistTestDir(t)
	members := []string{"alice@hermex.test", "bob@hermex.test"}

	t.Run("all: anyone may post", func(t *testing.T) {
		mkList(t, d, "all@hermex.test", mlistTypeNormal, mlistPrivAll, members...)
		wantExpands(t, d, "all@hermex.test", "stranger@elsewhere.test", members)
	})

	t.Run("internal: only a member may post", func(t *testing.T) {
		mkList(t, d, "int@hermex.test", mlistTypeNormal, mlistPrivInternal, members...)
		wantExpands(t, d, "int@hermex.test", "alice@hermex.test", members)
		wantRefuses(t, d, "int@hermex.test", "carol@hermex.test", MListPrivilInternal)
	})

	t.Run("domain: only same-domain senders", func(t *testing.T) {
		mkList(t, d, "dom@hermex.test", mlistTypeNormal, mlistPrivDomain, members...)
		wantExpands(t, d, "dom@hermex.test", "carol@hermex.test", members)
		wantRefuses(t, d, "dom@hermex.test", "dave@partner.test", MListPrivilDomain)
	})

	t.Run("specified: only named senders or domains", func(t *testing.T) {
		mkList(t, d, "spec@hermex.test", mlistTypeNormal, mlistPrivSpecified, members...)
		_, err := d.SetSpecifieds("spec@hermex.test", []string{"dave@partner.test", "elsewhere.test"})
		mustNoErr(t, "set the specifieds", err)
		wantExpands(t, d, "spec@hermex.test", "dave@partner.test", members)
		wantExpands(t, d, "spec@hermex.test", "anyone@elsewhere.test", members) // domain match
		wantRefuses(t, d, "spec@hermex.test", "carol@hermex.test", MListPrivilSpecified)
	})
}

// wantExpands checks a sender the privilege admits receives the whole member set.
func wantExpands(t *testing.T, d *SQLDirectory, list, from string, members []string) {
	t.Helper()
	got, res := expandSorted(t, d, list, from)
	wantEq(t, from+" posting to "+list, res, MListOK)
	if !slices.Equal(got, members) {
		t.Errorf("%s expanded to %v, want %v", list, got, members)
	}
}

// wantRefuses checks a sender the privilege refuses gets the matching code and NO
// members, so the MTA can bounce without fanning out.
func wantRefuses(t *testing.T, d *SQLDirectory, list, from string, want MListResult) {
	t.Helper()
	got, res := expandSorted(t, d, list, from)
	wantEq(t, from+" posting to "+list, res, want)
	wantEq(t, "members returned on a refusal", len(got), 0)
}

// TestExpandMListTypes pins the two live list types: normal returns its explicit
// members verbatim (a sub-list member is NOT recursed, that is the caller's job),
// and domain returns every mailbox user in the list's domain (and only mailbox
// users, not the list itself or other lists).
func TestExpandMListTypes(t *testing.T) {
	d := mlistTestDir(t)

	t.Run("normal returns explicit members, sub-list verbatim", func(t *testing.T) {
		mkList(t, d, "team@hermex.test", mlistTypeNormal, mlistPrivAll)
		mkList(t, d, "nested@hermex.test", mlistTypeNormal, mlistPrivAll, "alice@hermex.test", "team@hermex.test")
		got, res := expandSorted(t, d, "nested@hermex.test", "x@y.test")
		want := []string{"alice@hermex.test", "team@hermex.test"} // team is returned, not expanded
		if res != MListOK || !slices.Equal(got, want) {
			t.Errorf("got (%v, %d), want (%v, OK)", got, res, want)
		}
	})

	t.Run("domain returns every mailbox user, not lists", func(t *testing.T) {
		mkList(t, d, "everyone@hermex.test", mlistTypeDomain, mlistPrivAll)
		got, res := expandSorted(t, d, "everyone@hermex.test", "x@y.test")
		want := []string{"alice@hermex.test", "bob@hermex.test", "carol@hermex.test"}
		if res != MListOK || !slices.Equal(got, want) {
			t.Errorf("got (%v, %d), want (%v, OK), distribution lists must be excluded", got, res, want)
		}
	})
}

// TestExpandMListNotAList proves a normal mailbox address (or an unknown one) is
// not a list, so the MTA falls through to ordinary recipient resolution.
func TestExpandMListNotAList(t *testing.T) {
	d := mlistTestDir(t)
	for _, addr := range []string{"alice@hermex.test", "ghost@hermex.test"} {
		if got, res := expandSorted(t, d, addr, "x@y.test"); res != MListNone || got != nil {
			t.Errorf("ExpandMList(%s) = (%v, %d), want (nil, MListNone)", addr, got, res)
		}
	}
}

// TestSearchGALIncludesDistlist proves a distribution list (which has no mailbox)
// appears in the GAL carrying DT_DISTLIST, while a mailbox user keeps DT_MAILUSER,
// so the address book can render each with the right object class.
func TestSearchGALIncludesDistlist(t *testing.T) {
	d := mlistTestDir(t)
	mkList(t, d, "team@hermex.test", mlistTypeNormal, mlistPrivAll)

	lists, err := d.SearchGAL("alice@hermex.test", "team", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 1 || lists[0].Address != "team@hermex.test" || lists[0].DisplayType != dtDistlist {
		t.Errorf("SearchGAL(team) = %+v, want one team@hermex.test with DisplayType DT_DISTLIST", lists)
	}
	users, err := d.SearchGAL("alice@hermex.test", "alice", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].DisplayType != dtMailuser {
		t.Errorf("SearchGAL(alice) = %+v, want alice with DisplayType DT_MAILUSER", users)
	}
}

// TestMListCRUD proves the create/list/membership/delete round-trip, including
// that deleting a list cascades its membership away.
func TestMListCRUD(t *testing.T) {
	d := mlistTestDir(t)
	mkList(t, d, "crew@hermex.test", mlistTypeNormal, mlistPrivAll, "alice@hermex.test", "bob@hermex.test")

	lists, err := d.ListMLists()
	if err != nil || len(lists) != 1 || lists[0].Listname != "crew@hermex.test" {
		t.Fatalf("ListMLists = %v, %v; want one crew list", lists, err)
	}
	members, err := d.ListMembers("crew@hermex.test")
	if err != nil || !slices.Equal(members, []string{"alice@hermex.test", "bob@hermex.test"}) {
		t.Fatalf("ListMembers = %v, %v", members, err)
	}

	ok, err := d.DeleteMList("crew@hermex.test")
	if err != nil || !ok {
		t.Fatalf("DeleteMList = %v, %v; want true", ok, err)
	}
	lists, _ = d.ListMLists()
	if len(lists) != 0 {
		t.Errorf("after delete, ListMLists = %v, want empty", lists)
	}
	// The cascade removed membership; expanding the gone address is now "not a list".
	if _, res, _ := d.ExpandMList("crew@hermex.test", "x@y.test"); res != MListNone {
		t.Errorf("expand of deleted list res = %d, want MListNone", res)
	}
}

// TestMListOwner proves a distribution list's owner (the Exchange managedBy
// attribute) is stored, surfaced in the list summary, cleared by an empty value, and
// reported not-found for an unknown list.
func TestMListOwner(t *testing.T) {
	d := mlistTestDir(t)
	mkList(t, d, "crew@hermex.test", mlistTypeNormal, mlistPrivAll)

	found, err := d.SetMListOwner("crew@hermex.test", "alice@hermex.test")
	mustNoErr(t, "set list owner", err)
	wantEq(t, "SetMListOwner found the list", found, true)
	wantEq(t, "stored owner", mustOneMList(t, d, "after setting the owner").Owner, "alice@hermex.test")

	// The GAL search that feeds the NSPI address book carries the owner too.
	gal, err := d.SearchGAL("alice@hermex.test", "crew", 20)
	mustNoErr(t, "search GAL", err)
	if len(gal) != 1 {
		t.Fatalf("SearchGAL = %+v, want the crew list", gal)
	}
	wantEq(t, "the GAL entry's owner", gal[0].Owner, "alice@hermex.test")

	// The webmail group-management surface lists the lists a user owns.
	owned, err := d.ListMListsOwnedBy("alice@hermex.test")
	mustNoErr(t, "list alice's lists", err)
	if len(owned) != 1 {
		t.Fatalf("ListMListsOwnedBy(alice) = %+v, want [crew]", owned)
	}
	wantEq(t, "the list alice owns", owned[0].Listname, "crew@hermex.test")
	notOwned, _ := d.ListMListsOwnedBy("bob@hermex.test")
	wantEq(t, "lists bob owns", len(notOwned), 0)

	_, err = d.SetMListOwner("crew@hermex.test", "")
	mustNoErr(t, "clear the owner", err)
	wantEq(t, "owner after clearing", mustOneMList(t, d, "after clearing the owner").Owner, "")

	ghost, _ := d.SetMListOwner("ghost@hermex.test", "alice@hermex.test")
	wantEq(t, "SetMListOwner found an unknown list", ghost, false)
}

// mustOneMList reads the mailing-list summary, requiring exactly one list.
func mustOneMList(t *testing.T, d *SQLDirectory, what string) MListInfo {
	t.Helper()
	lists, err := d.ListMLists()
	mustNoErr(t, "list mailing lists", err)
	if len(lists) != 1 {
		t.Fatalf("lists %s = %+v, want one", what, lists)
	}
	return lists[0]
}

// TestUpsertLDAPGroup proves the group-sync authority path creates an LDAP-mastered
// list with an owner and exact membership, that a second run mirrors the membership
// (removing a dropped member), and that manual owner/membership edits on a mastered
// list are refused (so the next sync cannot silently overwrite them).
func TestUpsertLDAPGroup(t *testing.T) {
	d := mlistTestDir(t)
	ext := []byte{0x01, 0x02, 0x03}
	syncGroup := func(members ...string) {
		t.Helper()
		_, err := d.UpsertLDAPGroup("eng@hermex.test", ext, "alice@hermex.test", members)
		mustNoErr(t, "sync the LDAP group", err)
	}

	syncGroup("bob@hermex.test", "carol@hermex.test")
	list := mustOneMList(t, d, "after the first sync")
	wantEq(t, "the list is LDAP-mastered", list.LDAPMastered, true)
	wantEq(t, "the list owner", list.Owner, "alice@hermex.test")
	wantMembers(t, d, "after the first sync", "bob@hermex.test", "carol@hermex.test")

	// Manual edits are refused on a mastered list.
	_, err := d.SetMembers("eng@hermex.test", []string{"x@hermex.test"})
	wantLDAPMastered(t, "SetMembers on a mastered list", err)
	_, err = d.SetMListOwner("eng@hermex.test", "bob@hermex.test")
	wantLDAPMastered(t, "SetMListOwner on a mastered list", err)

	// A second sync mirrors membership exactly (carol dropped).
	syncGroup("bob@hermex.test")
	wantMembers(t, d, "after the re-sync", "bob@hermex.test")
}

// wantMembers checks a mailing list's membership against the exact set wanted.
func wantMembers(t *testing.T, d *SQLDirectory, what string, want ...string) {
	t.Helper()
	members, err := d.ListMembers("eng@hermex.test")
	mustNoErr(t, "list the members", err)
	if !slices.Equal(members, want) {
		t.Errorf("members %s = %v, want %v", what, members, want)
	}
}

// wantLDAPMastered checks an edit was refused because the list is LDAP-mastered.
func wantLDAPMastered(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, ErrLDAPMastered) {
		t.Errorf("%s: err = %v, want ErrLDAPMastered", what, err)
	}
}

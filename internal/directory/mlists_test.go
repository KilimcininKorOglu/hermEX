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
		if did, err := d.CreateDomain(dom, filepath.Join(root, "domains", dom)); err != nil || did <= 0 {
			t.Fatalf("create domain %s: id=%d err=%v", dom, did, err)
		}
	}
	for _, u := range []string{"alice@hermex.test", "bob@hermex.test", "carol@hermex.test", "dave@partner.test"} {
		if uid, err := d.CreateUser(u, "password1", filepath.Join(root, "users", u)); err != nil || uid <= 0 {
			t.Fatalf("create user %s: id=%d err=%v", u, uid, err)
		}
	}
	return d
}

// mkList creates a list of the given type and privilege with the given members.
func mkList(t *testing.T, d *SQLDirectory, addr string, listType, listPriv int, members ...string) {
	t.Helper()
	if lid, err := d.CreateMList(addr, listType, listPriv); err != nil || lid <= 0 {
		t.Fatalf("create list %s: id=%d err=%v", addr, lid, err)
	}
	if len(members) > 0 {
		if ok, err := d.SetMembers(addr, members); err != nil || !ok {
			t.Fatalf("set members of %s: ok=%v err=%v", addr, ok, err)
		}
	}
}

// TestSetMembersRefusesAnUnroutableMember proves a member set holding one address no
// relay can route is refused whole, and the list keeps its members: each member is
// a recipient of every post, so a bad one would bounce on each message.
func TestSetMembersRefusesAnUnroutableMember(t *testing.T) {
	d := mlistTestDir(t)
	mkList(t, d, "team@hermex.test", 0, 0, "alice@hermex.test")
	for _, bad := range []string{"bob", "bob@", "Bob <bob@hermex.test>", "a@b@hermex.test"} {
		if ok, err := d.SetMembers("team@hermex.test", []string{"carol@hermex.test", bad}); err == nil || ok {
			t.Errorf("SetMembers with %q = (%v, %v), want refused", bad, ok, err)
		}
	}
	got, err := d.ListMembers("team@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"alice@hermex.test"}) {
		t.Errorf("members after the refusals = %v, want [alice@hermex.test]", got)
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
		ok, err := d.SetSpecifieds("spec@hermex.test", []string{"dave@partner.test", "elsewhere.test"})
		mustNoErr(t, "set the specifieds", err)
		wantEq(t, "SetSpecifieds found list", ok, true)
		wantExpands(t, d, "spec@hermex.test", "dave@partner.test", members)
		wantExpands(t, d, "spec@hermex.test", "anyone@elsewhere.test", members) // domain match
		wantRefuses(t, d, "spec@hermex.test", "carol@hermex.test", MListPrivilSpecified)
	})

	// The sender is whatever MAIL FROM said, so the domain checks compare whole
	// domains: a domain that only ends with the allowed one, or contains it as a
	// label, is a different domain. Case does not make a different address.
	t.Run("lookalike domains are refused", func(t *testing.T) {
		mkList(t, d, "dom2@hermex.test", mlistTypeNormal, mlistPrivDomain, members...)
		wantRefuses(t, d, "dom2@hermex.test", "x@hermex.test.evil.example", MListPrivilDomain)
		wantRefuses(t, d, "dom2@hermex.test", "x@nothermex.test", MListPrivilDomain)
		wantExpands(t, d, "dom2@hermex.test", "Carol@HERMEX.TEST", members)
		mkList(t, d, "spec2@hermex.test", mlistTypeNormal, mlistPrivSpecified, members...)
		ok, err := d.SetSpecifieds("spec2@hermex.test", []string{"elsewhere.test"})
		mustNoErr(t, "set the specifieds", err)
		wantEq(t, "SetSpecifieds found spec2", ok, true)
		wantRefuses(t, d, "spec2@hermex.test", "x@notelsewhere.test", MListPrivilSpecified)
		wantRefuses(t, d, "spec2@hermex.test", "x@elsewhere.test.evil.example", MListPrivilSpecified)
		wantExpands(t, d, "spec2@hermex.test", "X@ElseWhere.Test", members)
		mkList(t, d, "int2@hermex.test", mlistTypeNormal, mlistPrivInternal, members...)
		wantExpands(t, d, "int2@hermex.test", "ALICE@hermex.test", members)
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

// TestCreateMListRefusesAnUnknownTypeOrPrivilege creates lists with a type outside
// the two live ones (normal 0, domain 2) and a privilege outside 0..4. Such a list
// expands to nothing or admits no sender, so its mail would vanish; each create is
// refused and no list exists afterwards.
func TestCreateMListRefusesAnUnknownTypeOrPrivilege(t *testing.T) {
	d := mlistTestDir(t)
	for _, c := range []struct{ typ, priv int }{{1, mlistPrivAll}, {-1, mlistPrivAll}, {mlistTypeNormal, 5}, {mlistTypeNormal, -1}} {
		if badID, err := d.CreateMList("bad@hermex.test", c.typ, c.priv); err == nil || badID != 0 {
			t.Errorf("type %d privilege %d returned id=%d err=%v, want error", c.typ, c.priv, badID, err)
		}
	}
	if got, res, err := d.ExpandMList("bad@hermex.test", "x@y.test"); err != nil || res != MListNone || len(got) != 0 {
		t.Errorf("a refused list exists: members=%v result=%d err=%v", got, res, err)
	}
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

	lists, err := d.SearchGAL("alice@hermex.test", "team", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 1 || lists[0].Address != "team@hermex.test" || lists[0].DisplayType != dtDistlist {
		t.Errorf("SearchGAL(team) = %+v, want one team@hermex.test with DisplayType DT_DISTLIST", lists)
	}
	users, err := d.SearchGAL("alice@hermex.test", "alice", 50, 0)
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
	lists, err = d.ListMLists()
	mustNoErr(t, "list after the delete", err)
	if len(lists) != 0 {
		t.Errorf("after delete, ListMLists = %v, want empty", lists)
	}
	// The cascade removed membership; expanding the gone address is now "not a list".
	goneMembers, res, err := d.ExpandMList("crew@hermex.test", "x@y.test")
	mustNoErr(t, "expand the deleted list", err)
	wantEq(t, "expand of the deleted list", res, MListNone)
	wantEq(t, "members of deleted list", len(goneMembers), 0)
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
	gal, err := d.SearchGAL("alice@hermex.test", "crew", 20, 0)
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
	notOwned, err := d.ListMListsOwnedBy("bob@hermex.test")
	mustNoErr(t, "list bob's lists", err)
	wantEq(t, "lists bob owns", len(notOwned), 0)

	cleared, err := d.SetMListOwner("crew@hermex.test", "")
	mustNoErr(t, "clear the owner", err)
	wantEq(t, "SetMListOwner clear found list", cleared, true)
	wantEq(t, "owner after clearing", mustOneMList(t, d, "after clearing the owner").Owner, "")

	ghost, err := d.SetMListOwner("ghost@hermex.test", "alice@hermex.test")
	mustNoErr(t, "set the owner of an unknown list", err)
	wantEq(t, "SetMListOwner found an unknown list", ghost, false)
}

// TestMListOwnerStaysInTheListsScope proves an owner is an account the list can
// reach: ownership lets that account manage the list's members from webmail, so
// it is a grant and stays inside the list's organization like every other one.
// A refused owner leaves the stored owner as it was.
func TestMListOwnerStaysInTheListsScope(t *testing.T) {
	d := mlistTestDir(t)
	mkList(t, d, "crew@hermex.test", mlistTypeNormal, mlistPrivAll)
	ok, err := d.SetMListOwner("crew@hermex.test", "alice@hermex.test")
	mustNoErr(t, "set the owner", err)
	wantEq(t, "set owner found list", ok, true)

	for _, bad := range []string{"dave@partner.test", "ghost@hermex.test", "bob", "Bob <bob@hermex.test>"} {
		badOK, err := d.SetMListOwner("crew@hermex.test", bad)
		if !errors.Is(err, ErrMListOwnerScope) {
			t.Errorf("owner %q: err = %v, want ErrMListOwnerScope", bad, err)
		}
		wantEq(t, "refused owner "+bad+" reported updated", badOK, false)
		wantEq(t, "the owner after refusing "+bad, mustOneMList(t, d, "after a refused owner").Owner, "alice@hermex.test")
	}

	ok, err = d.SetMListOwner("crew@hermex.test", "  BOB@Hermex.Test ")
	mustNoErr(t, "set a padded mixed-case owner", err)
	wantEq(t, "normalized owner set found list", ok, true)
	wantEq(t, "the normalized owner", mustOneMList(t, d, "after a padded owner").Owner, "bob@hermex.test")
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
	syncGroup := func(wantCreated bool, members ...string) {
		t.Helper()
		created, err := d.UpsertLDAPGroup("eng@hermex.test", ext, "alice@hermex.test", members)
		mustNoErr(t, "sync the LDAP group", err)
		wantEq(t, "LDAP group sync created", created, wantCreated)
	}

	syncGroup(true, "bob@hermex.test", "carol@hermex.test")
	list := mustOneMList(t, d, "after the first sync")
	wantEq(t, "the list is LDAP-mastered", list.LDAPMastered, true)
	wantEq(t, "the list owner", list.Owner, "alice@hermex.test")
	wantMembers(t, d, "after the first sync", "bob@hermex.test", "carol@hermex.test")

	// Manual edits are refused on a mastered list.
	okMem, err := d.SetMembers("eng@hermex.test", []string{"x@hermex.test"})
	wantLDAPMastered(t, "SetMembers on a mastered list", err)
	wantEq(t, "SetMembers on mastered reported updated", okMem, false)
	okOwn, err := d.SetMListOwner("eng@hermex.test", "bob@hermex.test")
	wantLDAPMastered(t, "SetMListOwner on a mastered list", err)
	wantEq(t, "SetMListOwner on mastered reported updated", okOwn, false)
	wantMembers(t, d, "after the refused edits", "bob@hermex.test", "carol@hermex.test")
	wantEq(t, "the owner after the refused edits", mustOneMList(t, d, "after the refused edits").Owner, "alice@hermex.test")

	// A second sync mirrors membership exactly (carol dropped).
	syncGroup(false, "bob@hermex.test")
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

package directory

import (
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
		got, res := expandSorted(t, d, "all@hermex.test", "stranger@elsewhere.test")
		if res != MListOK || !slices.Equal(got, members) {
			t.Errorf("got (%v, %d), want (%v, OK)", got, res, members)
		}
	})

	t.Run("internal: only a member may post", func(t *testing.T) {
		mkList(t, d, "int@hermex.test", mlistTypeNormal, mlistPrivInternal, members...)
		if got, res := expandSorted(t, d, "int@hermex.test", "alice@hermex.test"); res != MListOK || !slices.Equal(got, members) {
			t.Errorf("member post got (%v, %d), want members+OK", got, res)
		}
		if got, res := expandSorted(t, d, "int@hermex.test", "carol@hermex.test"); res != MListPrivilInternal || got != nil {
			t.Errorf("non-member post got (%v, %d), want (nil, PRIVIL_INTERNAL)", got, res)
		}
	})

	t.Run("domain: only same-domain senders", func(t *testing.T) {
		mkList(t, d, "dom@hermex.test", mlistTypeNormal, mlistPrivDomain, members...)
		if _, res := expandSorted(t, d, "dom@hermex.test", "carol@hermex.test"); res != MListOK {
			t.Errorf("same-domain post res = %d, want OK", res)
		}
		if got, res := expandSorted(t, d, "dom@hermex.test", "dave@partner.test"); res != MListPrivilDomain || got != nil {
			t.Errorf("cross-domain post got (%v, %d), want (nil, PRIVIL_DOMAIN)", got, res)
		}
	})

	t.Run("specified: only named senders or domains", func(t *testing.T) {
		mkList(t, d, "spec@hermex.test", mlistTypeNormal, mlistPrivSpecified, members...)
		if _, err := d.SetSpecifieds("spec@hermex.test", []string{"dave@partner.test", "elsewhere.test"}); err != nil {
			t.Fatal(err)
		}
		if _, res := expandSorted(t, d, "spec@hermex.test", "dave@partner.test"); res != MListOK {
			t.Errorf("named sender res = %d, want OK", res)
		}
		if _, res := expandSorted(t, d, "spec@hermex.test", "anyone@elsewhere.test"); res != MListOK {
			t.Errorf("named-domain sender res = %d, want OK (domain match)", res)
		}
		if got, res := expandSorted(t, d, "spec@hermex.test", "carol@hermex.test"); res != MListPrivilSpecified || got != nil {
			t.Errorf("unlisted sender got (%v, %d), want (nil, PRIVIL_SPECIFIED)", got, res)
		}
	})
}

// TestExpandMListTypes pins the two live list types: normal returns its explicit
// members verbatim (a sub-list member is NOT recursed — that is the caller's job),
// and domain returns every mailbox user in the list's domain (and only mailbox
// users — not the list itself or other lists).
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
			t.Errorf("got (%v, %d), want (%v, OK) — distribution lists must be excluded", got, res, want)
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

	lists, err := d.SearchGAL("team", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 1 || lists[0].Address != "team@hermex.test" || lists[0].DisplayType != dtDistlist {
		t.Errorf("SearchGAL(team) = %+v, want one team@hermex.test with DisplayType DT_DISTLIST", lists)
	}
	users, err := d.SearchGAL("alice", 50)
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

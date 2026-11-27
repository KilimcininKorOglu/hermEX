package directory

import "testing"

func hasUser(us []UserInfo, name string) bool {
	for _, u := range us {
		if u.Username == name {
			return true
		}
	}
	return false
}

// TestListInDomainScoping proves the per-domain list methods return only the
// requested domain's rows, the directory backend for the admin domain accordion.
func TestListInDomainScoping(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	id1 := mustCreateDomain(t, d, root, "one.test")
	id2 := mustCreateDomain(t, d, root, "two.test")
	seedUser(t, d, root, "a@one.test", "password1")
	seedUser(t, d, root, "b@two.test", "password1")
	cid, err := d.CreateContact("c@one.test", "C", "one.test")
	mustNoErr(t, "create contact", err)
	wantEq(t, "created contact id > 0", cid > 0, true)
	lid, err := d.CreateMList("list@two.test", 0, 0)
	mustNoErr(t, "create mailing list", err)
	wantEq(t, "created mlist id > 0", lid > 0, true)

	// Users scoped to their own domain.
	u1, err := d.ListUsersInDomain(id1)
	mustNoErr(t, "list users in domain one", err)
	wantEq(t, "a@one.test listed under its own domain", hasUser(u1, "a@one.test"), true)
	wantEq(t, "b@two.test listed under another domain", hasUser(u1, "b@two.test"), false)

	// Contacts scoped: only domain one has one.
	c1 := mustListContacts(t, d, id1)
	if len(c1) != 1 {
		t.Fatalf("ListContactsInDomain(one) = %v, want only c@one.test", c1)
	}
	wantEq(t, "the contact listed under domain one", c1[0].Address, "c@one.test")
	wantEq(t, "contacts under domain two", len(mustListContacts(t, d, id2)), 0)

	// Mailing lists scoped: only domain two has one.
	m2 := mustListMLists(t, d, id2)
	if len(m2) != 1 {
		t.Fatalf("ListMListsInDomain(two) = %v, want only list@two.test", m2)
	}
	wantEq(t, "the list under domain two", m2[0].Listname, "list@two.test")
	wantEq(t, "mailing lists under domain one", len(mustListMLists(t, d, id1)), 0)

	// An id no domain holds lists nothing at all.
	ghost, err := d.ListUsersInDomain(id2 + 1000)
	mustNoErr(t, "list users of an unknown domain", err)
	wantEq(t, "users under an unknown domain", len(ghost), 0)
	wantEq(t, "contacts under an unknown domain", len(mustListContacts(t, d, id2+1000)), 0)
	wantEq(t, "mailing lists under an unknown domain", len(mustListMLists(t, d, id2+1000)), 0)
}

// mustListContacts lists one domain's contacts.
func mustListContacts(t *testing.T, d *SQLDirectory, domainID int64) []ContactInfo {
	t.Helper()
	got, err := d.ListContactsInDomain(domainID)
	mustNoErr(t, "list contacts in domain", err)
	return got
}

// mustListMLists lists one domain's mailing lists.
func mustListMLists(t *testing.T, d *SQLDirectory, domainID int64) []MListInfo {
	t.Helper()
	got, err := d.ListMListsInDomain(domainID)
	mustNoErr(t, "list mailing lists in domain", err)
	return got
}

package directory

import (
	"path/filepath"
	"testing"
)

func hasUser(us []UserInfo, name string) bool {
	for _, u := range us {
		if u.Username == name {
			return true
		}
	}
	return false
}

// TestListInDomainScoping proves the per-domain list methods return only the
// requested domain's rows — the directory backend for the admin domain accordion.
func TestListInDomainScoping(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()

	id1, err := d.CreateDomain("one.test", filepath.Join(root, "one"))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := d.CreateDomain("two.test", filepath.Join(root, "two"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("a@one.test", "pw", filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("b@two.test", "pw", filepath.Join(root, "b")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateContact("c@one.test", "C", "one.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateMList("list@two.test", 0, 0); err != nil {
		t.Fatal(err)
	}

	// Users scoped to their own domain.
	u1, err := d.ListUsersInDomain(id1)
	if err != nil {
		t.Fatal(err)
	}
	if !hasUser(u1, "a@one.test") || hasUser(u1, "b@two.test") {
		t.Errorf("ListUsersInDomain(one) = %v, want a@one.test present and b@two.test absent", u1)
	}

	// Contacts scoped: only domain one has one.
	if c1, err := d.ListContactsInDomain(id1); err != nil || len(c1) != 1 || c1[0].Address != "c@one.test" {
		t.Fatalf("ListContactsInDomain(one) = %v (err %v), want only c@one.test", c1, err)
	}
	if c2, err := d.ListContactsInDomain(id2); err != nil || len(c2) != 0 {
		t.Fatalf("ListContactsInDomain(two) = %v (err %v), want none", c2, err)
	}

	// Mailing lists scoped: only domain two has one.
	if m2, err := d.ListMListsInDomain(id2); err != nil || len(m2) != 1 || m2[0].Listname != "list@two.test" {
		t.Fatalf("ListMListsInDomain(two) = %v (err %v), want only list@two.test", m2, err)
	}
	if m1, err := d.ListMListsInDomain(id1); err != nil || len(m1) != 0 {
		t.Fatalf("ListMListsInDomain(one) = %v (err %v), want none", m1, err)
	}
}

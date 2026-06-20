package directory

import (
	"path/filepath"
	"testing"
)

// contactTestDir builds a directory with one active domain and one mailbox user,
// ready for CreateContact calls. The user (a dt=0 row) is there so a dt=6 contact
// has a mailbox account to be contrasted against.
func contactTestDir(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	root := t.TempDir()
	if _, err := d.CreateDomain("hermex.test", filepath.Join(root, "domains", "hermex.test")); err != nil {
		t.Fatalf("create domain: %v", err)
	}
	if _, err := d.CreateUser("alice@hermex.test", "pw", filepath.Join(root, "users", "alice")); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return d
}

// galEntryFor returns the GAL entry whose address matches addr, failing when none
// does.
func galEntryFor(t *testing.T, d *SQLDirectory, addr string) GALEntry {
	t.Helper()
	entries, err := d.SearchGAL(addr, 20)
	if err != nil {
		t.Fatalf("SearchGAL(%q): %v", addr, err)
	}
	for _, e := range entries {
		if e.Address == addr {
			return e
		}
	}
	t.Fatalf("SearchGAL(%q) returned no entry for %q (got %+v)", addr, addr, entries)
	return GALEntry{}
}

// TestCreateContactAppearsInGAL is the deliverable: a created mail contact
// surfaces in the GAL as a DT_REMOTE_MAILUSER (display type 6) carrying its
// display name, which is what routes it into the NSPI "All Contacts" container. A
// contact with an external address filed under a local domain must still appear —
// the GAL is org-wide and the contact owns no mailbox.
func TestCreateContactAppearsInGAL(t *testing.T) {
	d := contactTestDir(t)
	if _, err := d.CreateContact("john@partner.example", "John Partner", "hermex.test"); err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	e := galEntryFor(t, d, "john@partner.example")
	if e.DisplayName != "John Partner" {
		t.Errorf("contact DisplayName = %q, want %q", e.DisplayName, "John Partner")
	}
	if e.DisplayType != dtContact {
		t.Errorf("contact DisplayType = %d, want %d (DT_REMOTE_MAILUSER)", e.DisplayType, dtContact)
	}
}

// TestCreateContactDomainMustExist pins that a contact is filed under a real local
// domain: the domain_id is a NOT NULL foreign key, so an unknown filing domain
// must be refused rather than producing an orphan row.
func TestCreateContactDomainMustExist(t *testing.T) {
	d := contactTestDir(t)
	if _, err := d.CreateContact("x@partner.example", "X", "nope.test"); err == nil {
		t.Fatal("CreateContact under a nonexistent domain should error")
	}
}

// TestUpdateContact renames a contact (PR_DISPLAY_NAME upsert), clears it back to
// the address fallback, and sets a name onto a contact that had none — the GAL
// reflects each.
func TestUpdateContact(t *testing.T) {
	d := contactTestDir(t)
	if _, err := d.CreateContact("john@partner.example", "John Partner", "hermex.test"); err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	if ok, err := d.UpdateContact("john@partner.example", "Jonathan Partner"); err != nil || !ok {
		t.Fatalf("UpdateContact rename = (%v, %v), want (true, nil)", ok, err)
	}
	if e := galEntryFor(t, d, "john@partner.example"); e.DisplayName != "Jonathan Partner" {
		t.Errorf("after rename DisplayName = %q, want Jonathan Partner", e.DisplayName)
	}
	// an empty name clears the property → the GAL falls back to the address
	if ok, err := d.UpdateContact("john@partner.example", "  "); err != nil || !ok {
		t.Fatalf("UpdateContact clear = (%v, %v), want (true, nil)", ok, err)
	}
	if e := galEntryFor(t, d, "john@partner.example"); e.DisplayName != "john@partner.example" {
		t.Errorf("after clear DisplayName = %q, want the address fallback", e.DisplayName)
	}
	// set a name onto a contact created without one
	if _, err := d.CreateContact("kate@vendor.example", "", "hermex.test"); err != nil {
		t.Fatalf("CreateContact kate: %v", err)
	}
	if ok, err := d.UpdateContact("kate@vendor.example", "Kate Vendor"); err != nil || !ok {
		t.Fatalf("UpdateContact set = (%v, %v), want (true, nil)", ok, err)
	}
	if e := galEntryFor(t, d, "kate@vendor.example"); e.DisplayName != "Kate Vendor" {
		t.Errorf("after set DisplayName = %q, want Kate Vendor", e.DisplayName)
	}
}

// TestUpdateContactGuard pins that UpdateContact only touches contacts: handed a
// mailbox user's address it reports not-found and writes nothing.
func TestUpdateContactGuard(t *testing.T) {
	d := contactTestDir(t)
	if ok, err := d.UpdateContact("alice@hermex.test", "Imposter"); err != nil || ok {
		t.Fatalf("UpdateContact on a mailbox user = (%v, %v), want (false, nil)", ok, err)
	}
	if e := galEntryFor(t, d, "alice@hermex.test"); e.DisplayName == "Imposter" {
		t.Error("UpdateContact wrote a display name onto a mailbox user it must not touch")
	}
}

// TestUpdateContactMissing reports not-found for an unknown address.
func TestUpdateContactMissing(t *testing.T) {
	d := contactTestDir(t)
	if ok, err := d.UpdateContact("nobody@nowhere.example", "X"); err != nil || ok {
		t.Fatalf("UpdateContact on an unknown address = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestDeleteContact round-trips a removal: the contact leaves the GAL, the call
// reports it removed one, and a second delete reports none.
func TestDeleteContact(t *testing.T) {
	d := contactTestDir(t)
	if _, err := d.CreateContact("john@partner.example", "John", "hermex.test"); err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	removed, err := d.DeleteContact("john@partner.example")
	if err != nil || !removed {
		t.Fatalf("DeleteContact = (%v, %v), want (true, nil)", removed, err)
	}
	entries, _ := d.SearchGAL("john@partner.example", 20)
	for _, e := range entries {
		if e.Address == "john@partner.example" {
			t.Errorf("deleted contact still in GAL: %+v", e)
		}
	}
	removed, err = d.DeleteContact("john@partner.example")
	if err != nil || removed {
		t.Fatalf("second DeleteContact = (%v, %v), want (false, nil)", removed, err)
	}
}

// TestDeleteContactLeavesMailboxUsers pins the display_type guard: DeleteContact
// must never remove a mailbox user even when handed a user's address, because a
// contact and a user are both users rows distinguished only by display_type.
func TestDeleteContactLeavesMailboxUsers(t *testing.T) {
	d := contactTestDir(t)
	removed, err := d.DeleteContact("alice@hermex.test")
	if err != nil || removed {
		t.Fatalf("DeleteContact on a mailbox user = (%v, %v), want (false, nil)", removed, err)
	}
	if _, ok := d.Resolve("alice@hermex.test"); !ok {
		t.Error("DeleteContact removed a mailbox user it must not touch")
	}
}

// TestListContacts returns exactly the contacts (not mailbox users), ordered by
// address, with the address standing in as display name when none is set.
func TestListContacts(t *testing.T) {
	d := contactTestDir(t)
	if _, err := d.CreateContact("john@partner.example", "John Partner", "hermex.test"); err != nil {
		t.Fatalf("CreateContact john: %v", err)
	}
	if _, err := d.CreateContact("kate@vendor.example", "", "hermex.test"); err != nil {
		t.Fatalf("CreateContact kate: %v", err)
	}
	got, err := d.ListContacts()
	if err != nil {
		t.Fatalf("ListContacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListContacts returned %d entries, want 2 (the mailbox user must not list): %+v", len(got), got)
	}
	if got[0].Address != "john@partner.example" || got[0].DisplayName != "John Partner" || got[0].Domain != "hermex.test" {
		t.Errorf("entry 0 = %+v, want john@partner.example / John Partner / hermex.test", got[0])
	}
	if got[1].Address != "kate@vendor.example" || got[1].DisplayName != "kate@vendor.example" {
		t.Errorf("entry 1 = %+v, want kate@vendor.example with the address as display name", got[1])
	}
}

// TestContactCannotAuthenticate pins the security invariant: a contact has no
// password and no mailbox, so it must never authenticate — the empty password
// must not unlock it.
func TestContactCannotAuthenticate(t *testing.T) {
	d := contactTestDir(t)
	if _, err := d.CreateContact("john@partner.example", "John", "hermex.test"); err != nil {
		t.Fatalf("CreateContact: %v", err)
	}
	if _, ok := d.Authenticate("john@partner.example", ""); ok {
		t.Error("a mail contact authenticated; contacts must never be able to log in")
	}
}

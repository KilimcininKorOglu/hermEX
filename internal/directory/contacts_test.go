package directory

import "testing"

// contactTestDir builds a directory with one active domain and one mailbox user,
// ready for CreateContact calls. The user (a dt=0 row) is there so a dt=6 contact
// has a mailbox account to be contrasted against.
func contactTestDir(t *testing.T) *SQLDirectory {
	t.Helper()
	d, _ := freshDirectory(t)
	root := t.TempDir()
	mustCreateDomain(t, d, root, "hermex.test")
	mustCreateUser(t, d, root, "alice@hermex.test", "pw")
	return d
}

// mustCreateContact files a mail contact under the test domain.
func mustCreateContact(t *testing.T, d *SQLDirectory, addr, name string) {
	t.Helper()
	_, err := d.CreateContact(addr, name, "hermex.test")
	mustNoErr(t, "create contact "+addr, err)
}

// mustUpdateContact renames a contact and requires the update to have found it.
func mustUpdateContact(t *testing.T, d *SQLDirectory, addr, name string) {
	t.Helper()
	ok, err := d.UpdateContact(addr, name)
	mustNoErr(t, "update contact "+addr, err)
	wantEq(t, "the update found "+addr, ok, true)
}

// wantGALName checks the display name the GAL reports for one address.
func wantGALName(t *testing.T, d *SQLDirectory, addr, want string) {
	t.Helper()
	wantEq(t, "the GAL display name of "+addr, galEntryFor(t, d, addr).DisplayName, want)
}

// galEntryFor returns the GAL entry whose address matches addr, failing when none
// does.
func galEntryFor(t *testing.T, d *SQLDirectory, addr string) GALEntry {
	t.Helper()
	entries, err := d.SearchGAL("alice@hermex.test", addr, 20)
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
// contact with an external address filed under a local domain must still appear,
// the GAL is org-wide and the contact owns no mailbox.
func TestCreateContactAppearsInGAL(t *testing.T) {
	d := contactTestDir(t)
	mustCreateContact(t, d, "john@partner.example", "John Partner")
	e := galEntryFor(t, d, "john@partner.example")
	wantEq(t, "the contact display name", e.DisplayName, "John Partner")
	wantEq(t, "the contact display type (DT_REMOTE_MAILUSER)", e.DisplayType, dtContact)
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
// the address fallback, and sets a name onto a contact that had none, the GAL
// reflects each.
func TestUpdateContact(t *testing.T) {
	d := contactTestDir(t)
	mustCreateContact(t, d, "john@partner.example", "John Partner")

	mustUpdateContact(t, d, "john@partner.example", "Jonathan Partner")
	wantGALName(t, d, "john@partner.example", "Jonathan Partner")

	// an empty name clears the property → the GAL falls back to the address
	mustUpdateContact(t, d, "john@partner.example", "  ")
	wantGALName(t, d, "john@partner.example", "john@partner.example")

	// set a name onto a contact created without one
	mustCreateContact(t, d, "kate@vendor.example", "")
	mustUpdateContact(t, d, "kate@vendor.example", "Kate Vendor")
	wantGALName(t, d, "kate@vendor.example", "Kate Vendor")
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
	mustCreateContact(t, d, "john@partner.example", "John")

	removed, err := d.DeleteContact("john@partner.example")
	mustNoErr(t, "delete the contact", err)
	wantEq(t, "the delete removed one", removed, true)

	entries, _ := d.SearchGAL("alice@hermex.test", "john@partner.example", 20)
	for _, e := range entries {
		if e.Address == "john@partner.example" {
			t.Errorf("deleted contact still in GAL: %+v", e)
		}
	}

	removed, err = d.DeleteContact("john@partner.example")
	mustNoErr(t, "delete the contact again", err)
	wantEq(t, "the second delete removed one", removed, false)
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
	mustCreateContact(t, d, "john@partner.example", "John Partner")
	mustCreateContact(t, d, "kate@vendor.example", "")

	got, err := d.ListContacts()
	mustNoErr(t, "list the contacts", err)
	if len(got) != 2 {
		t.Fatalf("ListContacts returned %d entries, want 2 (the mailbox user must not list): %+v", len(got), got)
	}
	wantEq(t, "entry 0 address", got[0].Address, "john@partner.example")
	wantEq(t, "entry 0 display name", got[0].DisplayName, "John Partner")
	wantEq(t, "entry 0 domain", got[0].Domain, "hermex.test")
	wantEq(t, "entry 1 address", got[1].Address, "kate@vendor.example")
	wantEq(t, "entry 1 display name (the address stands in)", got[1].DisplayName, "kate@vendor.example")
}

// TestContactCannotAuthenticate pins the security invariant: a contact has no
// password and no mailbox, so it must never authenticate, the empty password
// must not unlock it.
func TestContactCannotAuthenticate(t *testing.T) {
	d := contactTestDir(t)
	mustCreateContact(t, d, "john@partner.example", "John")
	if _, ok := d.Authenticate("john@partner.example", ""); ok {
		t.Error("a mail contact authenticated; contacts must never be able to log in")
	}
}

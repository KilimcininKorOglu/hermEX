package directory

import (
	"errors"
	"testing"
)

// syncedContactID is the stable identifier a downsync would carry from the directory.
var syncedContactID = []byte{0xAA, 0xBB}

// mustUpsertLDAPContact applies a downsynced contact and requires it to succeed.
func mustUpsertLDAPContact(t *testing.T, d *SQLDirectory, addr, name string) bool {
	t.Helper()
	created, err := d.UpsertLDAPContact(addr, syncedContactID, name, "hermex.test")
	mustNoErr(t, "upsert the LDAP contact "+addr, err)
	return created
}

// TestUpsertLDAPContactCreates proves a downsynced contact lands in the GAL under its
// display name, which is what an operator sees in the address book.
func TestUpsertLDAPContactCreates(t *testing.T) {
	d := contactTestDir(t)

	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc")

	wantGALName(t, d, "partner@remote.example", "Partner Inc")
}

// TestUpsertLDAPContactReportsCreation proves the first sync reports a creation, so the
// summary counts a new contact once.
func TestUpsertLDAPContactReportsCreation(t *testing.T) {
	d := contactTestDir(t)

	created := mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc")

	wantEq(t, "the first upsert created the contact", created, true)
}

// TestUpsertLDAPContactUpdatesTheSecondTime proves a repeated sync updates rather than
// creating a duplicate row.
func TestUpsertLDAPContactUpdatesTheSecondTime(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc")

	created := mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner International")

	wantEq(t, "the second upsert updated the contact", created, false)
}

// TestUpsertLDAPContactRenames proves a display name changed in the directory reaches the
// local contact on the next sync.
func TestUpsertLDAPContactRenames(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc")

	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner International")

	wantGALName(t, d, "partner@remote.example", "Partner International")
}

// TestUpsertLDAPContactRefusesAMailboxUser is the load-bearing guard: a directory contact
// whose address collides with a local mailbox must be refused, because converting the row
// would take a real user's mailbox away.
func TestUpsertLDAPContactRefusesAMailboxUser(t *testing.T) {
	d := contactTestDir(t)

	if _, err := d.UpsertLDAPContact("alice@hermex.test", syncedContactID, "Imposter", "hermex.test"); err == nil {
		t.Fatal("upserting a contact over a mailbox user must be an error")
	}
}

// TestUpsertLDAPContactLeavesTheMailboxIntact proves the refused upsert wrote nothing: the
// mailbox user still resolves.
func TestUpsertLDAPContactLeavesTheMailboxIntact(t *testing.T) {
	d := contactTestDir(t)
	_, _ = d.UpsertLDAPContact("alice@hermex.test", syncedContactID, "Imposter", "hermex.test")

	if _, ok := d.Resolve("alice@hermex.test"); !ok {
		t.Error("the refused upsert removed or converted a mailbox user")
	}
}

// TestListContactsReportsTheLDAPID proves the listing carries the identifier the admin
// panel shows, so an operator can tell a synced contact from a hand-made one.
func TestListContactsReportsTheLDAPID(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc")

	got, err := d.ListContacts()
	mustNoErr(t, "list the contacts", err)

	wantEq(t, "the synced contact's LDAP id", got[0].LDAPID, "aabb")
}

// TestListContactsLeavesAHandMadeContactWithoutAnID proves a contact an operator created
// reports no LDAP id, which is what makes the prune pass skip it.
func TestListContactsLeavesAHandMadeContactWithoutAnID(t *testing.T) {
	d := contactTestDir(t)
	mustCreateContact(t, d, "local@partner.example", "Local")

	got, err := d.ListContacts()
	mustNoErr(t, "list the contacts", err)

	wantEq(t, "the hand-made contact's LDAP id", got[0].LDAPID, "")
}

// TestUpdateContactRefusesAMasteredContact proves a manual rename of a synced contact is
// refused rather than silently reverted by the next sync.
func TestUpdateContactRefusesAMasteredContact(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc")

	_, err := d.UpdateContact("partner@remote.example", "Renamed")

	if !errors.Is(err, ErrLDAPMasteredContact) {
		t.Errorf("UpdateContact on a synced contact = %v, want ErrLDAPMasteredContact", err)
	}
}

// TestDeleteContactRefusesAMasteredContact proves a manual delete of a synced contact is
// refused, because the next sync would recreate it.
func TestDeleteContactRefusesAMasteredContact(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc")

	_, err := d.DeleteContact("partner@remote.example")

	if !errors.Is(err, ErrLDAPMasteredContact) {
		t.Errorf("DeleteContact on a synced contact = %v, want ErrLDAPMasteredContact", err)
	}
}

// TestDeleteLDAPContactRemovesASyncedContact proves the prune path removes a contact the
// directory no longer publishes.
func TestDeleteLDAPContactRemovesASyncedContact(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc")

	removed, err := d.DeleteLDAPContact("partner@remote.example")
	mustNoErr(t, "prune the synced contact", err)

	wantEq(t, "the prune removed the contact", removed, true)
}

// TestDeleteLDAPContactKeepsAHandMadeContact proves the prune path never removes a contact
// an operator created, which carries no LDAP id.
func TestDeleteLDAPContactKeepsAHandMadeContact(t *testing.T) {
	d := contactTestDir(t)
	mustCreateContact(t, d, "local@partner.example", "Local")

	removed, err := d.DeleteLDAPContact("local@partner.example")
	mustNoErr(t, "prune a hand-made contact", err)

	wantEq(t, "the prune left the hand-made contact", removed, false)
}

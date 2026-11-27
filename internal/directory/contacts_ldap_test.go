package directory

import (
	"errors"
	"testing"
)

// syncedContactID is the stable identifier a downsync would carry from the directory.
var syncedContactID = []byte{0xAA, 0xBB}

// mustUpsertLDAPContact applies a downsynced contact, requires it to succeed, and
// checks whether it reported a creation (true) or an update of the existing row.
func mustUpsertLDAPContact(t *testing.T, d *SQLDirectory, addr, name string, wantCreated bool) {
	t.Helper()
	created, err := d.UpsertLDAPContact(addr, syncedContactID, name, "hermex.test")
	mustNoErr(t, "upsert the LDAP contact "+addr, err)
	wantEq(t, "the upsert of "+addr+" created a contact", created, wantCreated)
}

// TestUpsertLDAPContactCreates proves a downsynced contact lands in the GAL under its
// display name, which is what an operator sees in the address book.
func TestUpsertLDAPContactCreates(t *testing.T) {
	d := contactTestDir(t)

	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc", true)

	wantGALName(t, d, "partner@remote.example", "Partner Inc")
}

// TestUpsertLDAPContactUpdatesTheSecondTime proves a repeated sync updates rather than
// creating a duplicate row, and reports the update so the summary counts the
// contact once.
func TestUpsertLDAPContactUpdatesTheSecondTime(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc", true)

	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner International", false)

	wantEq(t, "contacts after two syncs", len(allContacts(t, d)), 1)
}

// allContacts lists every contact.
func allContacts(t *testing.T, d *SQLDirectory) []ContactInfo {
	t.Helper()
	got, err := d.ListContacts()
	mustNoErr(t, "list the contacts", err)
	return got
}

// onlyContact lists the contacts and requires exactly one.
func onlyContact(t *testing.T, d *SQLDirectory) ContactInfo {
	t.Helper()
	got := allContacts(t, d)
	if len(got) != 1 {
		t.Fatalf("contacts = %+v, want exactly one", got)
	}
	return got[0]
}

// TestUpsertLDAPContactRenames proves a display name changed in the directory reaches the
// local contact on the next sync.
func TestUpsertLDAPContactRenames(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc", true)

	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner International", false)

	wantGALName(t, d, "partner@remote.example", "Partner International")
}

// TestUpsertLDAPContactRefusesAMailboxUser is the load-bearing guard: a directory contact
// whose address collides with a local mailbox must be refused, because converting the row
// would take a real user's mailbox away. The refused upsert writes nothing: the mailbox
// user still resolves.
func TestUpsertLDAPContactRefusesAMailboxUser(t *testing.T) {
	d := contactTestDir(t)

	if created, err := d.UpsertLDAPContact("alice@hermex.test", syncedContactID, "Imposter", "hermex.test"); err == nil || created {
		t.Fatalf("upserting a contact over a mailbox user = (created=%v, err=%v), want error", created, err)
	}

	wantEq(t, "contacts after the refused upsert", len(allContacts(t, d)), 0)
	if path, ok := d.Resolve("alice@hermex.test"); !ok || path == "" {
		t.Errorf("the refused upsert removed or converted a mailbox user (%q, %v)", path, ok)
	}
}

// TestUpsertLDAPContactRefusesAnUnroutableAddress proves a directory entry is held
// to the same addr-spec rule as a hand-made contact: an LDAP server is outside the
// program, and a mail attribute carrying a display name, a list or a header would
// be offered to every client as a recipient.
func TestUpsertLDAPContactRefusesAnUnroutableAddress(t *testing.T) {
	d := contactTestDir(t)
	for _, addr := range []string{"Partner <partner@remote.example>", "a@remote.example, b@remote.example",
		"partner@remote.example\r\nBcc: x@evil.example", "partner@", ""} {
		created, err := d.UpsertLDAPContact(addr, syncedContactID, "Partner", "hermex.test")
		wantErr(t, "an unroutable LDAP address was accepted: "+addr, err)
		wantEq(t, "refused LDAP address created a contact: "+addr, created, false)
	}
	wantEq(t, "contacts after the refused upserts", len(allContacts(t, d)), 0)
}

// TestUpsertLDAPContactRefusesOtherDomain proves one domain's sync cannot take over a
// contact another domain's sync filed, and leaves the row where it was.
func TestUpsertLDAPContactRefusesOtherDomain(t *testing.T) {
	d := contactTestDir(t)
	seedDomain(t, d, t.TempDir(), "other.test")
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc", true)

	created, err := d.UpsertLDAPContact("partner@remote.example", syncedContactID, "Taken", "other.test")

	wantEq(t, "the takeover is refused", errors.Is(err, ErrContactInOtherDomain), true)
	wantEq(t, "refused takeover did not create", created, false)
	wantEq(t, "the contact's domain", onlyContact(t, d).Domain, "hermex.test")
	wantGALName(t, d, "partner@remote.example", "Partner Inc")
}

// TestListContactsReportsTheLDAPID proves the listing carries the identifier the admin
// panel shows (the hex of the directory's stable id), so an operator can tell a synced
// contact from a hand-made one.
func TestListContactsReportsTheLDAPID(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc", true)

	wantEq(t, "the synced contact's LDAP id", onlyContact(t, d).LDAPID, "aabb")
}

// TestListContactsLeavesAHandMadeContactWithoutAnID proves a contact an operator created
// reports no LDAP id, which is what makes the prune pass skip it.
func TestListContactsLeavesAHandMadeContactWithoutAnID(t *testing.T) {
	d := contactTestDir(t)
	mustCreateContact(t, d, "local@partner.example", "Local")

	wantEq(t, "the hand-made contact's LDAP id", onlyContact(t, d).LDAPID, "")
}

// TestUpdateContactRefusesAMasteredContact proves a manual rename of a synced contact is
// refused rather than silently reverted by the next sync.
func TestUpdateContactRefusesAMasteredContact(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc", true)

	updated, err := d.UpdateContact("partner@remote.example", "Renamed")

	if !errors.Is(err, ErrLDAPMasteredContact) || updated {
		t.Errorf("UpdateContact on a synced contact = (%v, %v), want false and ErrLDAPMasteredContact", updated, err)
	}
	wantGALName(t, d, "partner@remote.example", "Partner Inc")
}

// TestDeleteContactRefusesAMasteredContact proves a manual delete of a synced contact is
// refused, because the next sync would recreate it.
func TestDeleteContactRefusesAMasteredContact(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc", true)

	deleted, err := d.DeleteContact("partner@remote.example")

	if !errors.Is(err, ErrLDAPMasteredContact) || deleted {
		t.Errorf("DeleteContact on a synced contact = (%v, %v), want false and ErrLDAPMasteredContact", deleted, err)
	}
	wantEq(t, "contacts after the refused delete", len(allContacts(t, d)), 1)
}

// TestDeleteLDAPContactRemovesASyncedContact proves the prune path removes a contact the
// directory no longer publishes.
func TestDeleteLDAPContactRemovesASyncedContact(t *testing.T) {
	d := contactTestDir(t)
	mustUpsertLDAPContact(t, d, "partner@remote.example", "Partner Inc", true)

	removed, err := d.DeleteLDAPContact("partner@remote.example")
	mustNoErr(t, "prune the synced contact", err)

	wantEq(t, "the prune removed the contact", removed, true)
	wantEq(t, "contacts after the prune", len(allContacts(t, d)), 0)
}

// TestDeleteLDAPContactKeepsAHandMadeContact proves the prune path never removes a contact
// an operator created, which carries no LDAP id.
func TestDeleteLDAPContactKeepsAHandMadeContact(t *testing.T) {
	d := contactTestDir(t)
	mustCreateContact(t, d, "local@partner.example", "Local")

	removed, err := d.DeleteLDAPContact("local@partner.example")
	mustNoErr(t, "prune a hand-made contact", err)

	wantEq(t, "the prune left the hand-made contact", removed, false)
	wantEq(t, "contacts after the refused prune", len(allContacts(t, d)), 1)
}

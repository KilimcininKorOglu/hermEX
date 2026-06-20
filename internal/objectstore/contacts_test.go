package objectstore

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// storeContact creates a contact in the Contacts folder carrying one e-mail
// address in the given slot (1..3), allocating that slot's Address named id.
func storeContact(t *testing.T, s *Store, slot int, name, addr string) {
	t.Helper()
	ids, err := s.GetNamedPropIDs(true, []mapi.PropertyName{contactEmailNames[slot-1]})
	if err != nil {
		t.Fatalf("allocate email%d named id: %v", slot, err)
	}
	emailTag := tag(ids[0], mapi.PtUnicode)
	if _, err := s.CreateMessage(int64(mapi.PrivateFIDContacts), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
		{Tag: mapi.PrDisplayName, Value: name},
		{Tag: emailTag, Value: addr},
	}}); err != nil {
		t.Fatalf("create contact %q: %v", name, err)
	}
}

// has is ContactHasAddress with the error fatal, for terse assertions.
func has(t *testing.T, s *Store, addr string) bool {
	t.Helper()
	ok, err := s.ContactHasAddress(addr)
	if err != nil {
		t.Fatalf("ContactHasAddress(%q): %v", addr, err)
	}
	return ok
}

// TestContactHasAddress proves the known-sender lookup that gates the
// out-of-office external audience: a stored contact's address matches
// case-insensitively and through a display-name form, a slot-2/3 address is found
// (all three e-mail slots are scanned), and a non-contact address does not match —
// the property that makes "external known only" actually withhold replies.
func TestContactHasAddress(t *testing.T) {
	s := openSeededStore(t)
	storeContact(t, s, 1, "Ada Lovelace", "ada@partner.example")
	storeContact(t, s, 2, "Grace Hopper", "grace@navy.example")

	if !has(t, s, "ada@partner.example") {
		t.Error("a stored contact address must match")
	}
	if !has(t, s, "ADA@Partner.Example") {
		t.Error("the match must be case-insensitive")
	}
	if !has(t, s, "Ada Lovelace <ada@partner.example>") {
		t.Error("a display-name form must reduce to the bare addr-spec and match")
	}
	if !has(t, s, "grace@navy.example") {
		t.Error("an address in the second e-mail slot must match — every slot is scanned")
	}
	if has(t, s, "stranger@elsewhere.example") {
		t.Error("an address belonging to no contact must NOT match, or known-only would reply to everyone")
	}
	if has(t, s, "") {
		t.Error("a blank address must never match")
	}
}

// TestContactHasAddressNoContacts proves a mailbox that has never stored a contact
// e-mail resolves no named ids and reports false without error — the fast path that
// keeps ordinary delivery free of a folder scan.
func TestContactHasAddressNoContacts(t *testing.T) {
	s := openSeededStore(t)
	if has(t, s, "anyone@example.test") {
		t.Error("a mailbox with no contacts must report no known address")
	}
}

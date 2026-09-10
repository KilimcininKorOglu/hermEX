package objectstore

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
)

// attachPhoto marks the contact at an address with a photo attachment, the shape
// webmail stores when the user uploads a contact picture.
func attachPhoto(t *testing.T, s *Store, addr string, data []byte) {
	t.Helper()
	id, ok, err := s.contactIDForAddress(addr)
	if err != nil || !ok {
		t.Fatalf("contact for %q not found: %v", addr, err)
	}
	if _, _, err := s.CreateAttachment(id, mapi.PropertyValues{
		{Tag: mapi.PrAttachDataBin, Value: data},
		{Tag: mapi.PrAttachFilename, Value: "ContactPicture.jpg"},
		{Tag: mapi.PrAttachmentContactPhoto, Value: true},
	}); err != nil {
		t.Fatalf("attach photo to %q: %v", addr, err)
	}
}

// TestContactByAddressCarriesTheName proves an address in any of the three slots
// resolves to its contact, which is what a persona lookup reports as the name.
func TestContactByAddressCarriesTheName(t *testing.T) {
	s := openSeededStore(t)
	storeContact(t, s, 1, "Ada Lovelace", "ada@partner.example")
	storeContact(t, s, 3, "Grace Hopper", "grace@navy.example")

	for _, c := range []struct{ addr, want string }{
		{"ada@partner.example", "Ada Lovelace"},
		{"ADA@Partner.Example", "Ada Lovelace"}, // the client sends what the user typed
		{"grace@navy.example", "Grace Hopper"},  // the third slot is searched too
	} {
		m, ok, err := s.ContactByAddress(c.addr)
		if err != nil || !ok {
			t.Errorf("ContactByAddress(%q) found nothing (err=%v)", c.addr, err)
			continue
		}
		if m.DisplayName != c.want {
			t.Errorf("ContactByAddress(%q) name = %q, want %q", c.addr, m.DisplayName, c.want)
		}
		if m.Address != c.addr {
			t.Errorf("ContactByAddress(%q) address = %q, want the address asked for", c.addr, m.Address)
		}
	}
}

// TestContactByAddressRefusesAStranger keeps the lookup honest: an address nobody
// saved has no contact, so a persona must not be invented for it.
func TestContactByAddressRefusesAStranger(t *testing.T) {
	s := openSeededStore(t)
	storeContact(t, s, 1, "Ada Lovelace", "ada@partner.example")

	for _, addr := range []string{"nobody@partner.example", ""} {
		if _, ok, err := s.ContactByAddress(addr); ok || err != nil {
			t.Errorf("ContactByAddress(%q) = %v (err=%v), want not found", addr, ok, err)
		}
	}
}

// TestContactPhotoForServesTheStoredPicture is the load-bearing case: the picture
// webmail stored on a contact card is the picture every other surface serves for
// that address.
func TestContactPhotoForServesTheStoredPicture(t *testing.T) {
	s := openSeededStore(t)
	storeContact(t, s, 1, "Ada Lovelace", "ada@partner.example")
	want := []byte("\xff\xd8\xff\xe0 jpeg bytes")
	attachPhoto(t, s, "ada@partner.example", want)

	got, err := s.ContactPhotoFor("ada@partner.example")
	if err != nil {
		t.Fatalf("ContactPhotoFor: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("photo = %q, want the stored bytes", got)
	}
}

// TestContactPhotoForWithoutAPicture proves the absent cases answer nothing rather
// than failing: a contact with no picture, and an address that is not a contact.
func TestContactPhotoForWithoutAPicture(t *testing.T) {
	s := openSeededStore(t)
	storeContact(t, s, 1, "Ada Lovelace", "ada@partner.example")

	for _, addr := range []string{"ada@partner.example", "nobody@partner.example"} {
		got, err := s.ContactPhotoFor(addr)
		if err != nil || got != nil {
			t.Errorf("ContactPhotoFor(%q) = %q (err=%v), want nothing", addr, got, err)
		}
	}
}

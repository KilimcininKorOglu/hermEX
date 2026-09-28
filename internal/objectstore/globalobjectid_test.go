package objectstore

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcical"
)

// storedGOID creates an appointment with the given properties and returns the
// PidLidGlobalObjectId it was stored with.
func storedGOID(t *testing.T, s *Store, props mapi.PropertyValues) []byte {
	t.Helper()
	props = append(props, mapi.TaggedPropVal{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"})
	id := createInFolder(t, s, int64(mapi.PrivateFIDCalendar), props)
	ids, err := s.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameGlobalObjectId})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetMessageProperties(id, mapi.MakeTag(ids[0], mapi.PtBinary))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := stored.Get(mapi.MakeTag(ids[0], mapi.PtBinary))
	b, _ := v.([]byte)
	return b
}

// TestAppointmentsGetAnIdentity proves every appointment is stored with a global
// object id ([MS-OXOCAL] 2.2.1.27): a distinct fresh one when the creator supplied
// no identity, the one its iCalendar UID names when it has a UID, and the
// creator's own id unchanged.
func TestAppointmentsGetAnIdentity(t *testing.T) {
	s := openSeededStore(t)
	a := storedGOID(t, s, nil)
	b := storedGOID(t, s, nil)
	if len(a) < 40 || bytes.Equal(a, b) {
		t.Errorf("two appointments without an identity got %x and %x, want two distinct ids", a, b)
	}

	ids, err := s.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameICalUID, mapi.NameGlobalObjectId})
	if err != nil {
		t.Fatal(err)
	}
	withUID := storedGOID(t, s, mapi.PropertyValues{{Tag: mapi.MakeTag(ids[0], mapi.PtUnicode), Value: "event-1@example.org"}})
	if !bytes.Equal(withUID, oxcical.GlobalObjectID("event-1@example.org")) {
		t.Errorf("an appointment with a UID got %x, not the id its UID names", withUID)
	}

	own := oxcical.GlobalObjectID("client-chosen")
	kept := storedGOID(t, s, mapi.PropertyValues{{Tag: mapi.MakeTag(ids[1], mapi.PtBinary), Value: own}})
	if !bytes.Equal(kept, own) {
		t.Errorf("the creator's global object id was replaced: %x", kept)
	}
}

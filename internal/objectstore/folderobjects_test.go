package objectstore

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// contactMsg builds a minimal IPM.Contact object for enumeration tests.
func contactMsg(name string) *oxcmail.Message {
	return &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
		{Tag: mapi.PrDisplayName, Value: name},
	}}
}

// TestListFolderObjects locks the distinction that motivates the primitive: an
// object created with CreateMessage is visible to ListFolderObjects (which reads
// the object store) but NOT to ListMessages (which reads the IMAP index), because
// non-mail items are never indexed. It also checks that change numbers are
// monotonic and that FolderMaxChangeNumber tracks the latest write.
func TestListFolderObjects(t *testing.T) {
	s := openSeededStore(t)

	// A fresh contacts folder enumerates nothing and has no change cursor.
	wantEq(t, "empty contacts object count", len(mustListFolderObjects(t, s, mapi.PrivateFIDContacts)), 0)
	wantEq(t, "empty contacts max change number", mustFolderMaxChangeNumber(t, s, mapi.PrivateFIDContacts), uint64(0))

	eid1 := mustCreateMessage(t, s, mapi.PrivateFIDContacts, contactMsg("Ada Lovelace"))
	eid2 := mustCreateMessage(t, s, mapi.PrivateFIDContacts, contactMsg("Grace Hopper"))

	objs := mustListFolderObjects(t, s, mapi.PrivateFIDContacts)
	if len(objs) != 2 {
		t.Fatalf("got %d objects, want 2", len(objs))
	}
	wantEq(t, "first object id (EID order)", objs[0].ID, eid1)
	wantEq(t, "second object id (EID order)", objs[1].ID, eid2)
	if objs[0].ChangeNumber == 0 || objs[1].ChangeNumber <= objs[0].ChangeNumber {
		t.Errorf("change numbers not monotonic: %d, %d", objs[0].ChangeNumber, objs[1].ChangeNumber)
	}

	// The IMAP index never saw these objects, this is why DAV needs the object
	// store primitive rather than ListMessages.
	wantEq(t, "contacts in the IMAP index", len(mustListMessages(t, s, mapi.PrivateFIDContacts)), 0)

	// The collection's sync cursor is the highest live change number.
	wantEq(t, "max change number", mustFolderMaxChangeNumber(t, s, mapi.PrivateFIDContacts), objs[1].ChangeNumber)
}

// mustListFolderObjects enumerates a folder through the object store.
func mustListFolderObjects(t *testing.T, s *Store, folderID int64) []FolderObject {
	t.Helper()
	objs, err := s.ListFolderObjects(folderID)
	mustNoErr(t, "list folder objects", err)
	return objs
}

// mustFolderMaxChangeNumber returns a folder's sync cursor.
func mustFolderMaxChangeNumber(t *testing.T, s *Store, folderID int64) uint64 {
	t.Helper()
	max, err := s.FolderMaxChangeNumber(folderID)
	mustNoErr(t, "folder max change number", err)
	return max
}

// TestDeleteObject deletes an object-store-only object (a contact) by EID, the
// path the DAV layer uses since such objects never enter the IMAP index and so
// have no UID for DeleteMessage.
func TestDeleteObject(t *testing.T) {
	s := openSeededStore(t)
	eid, err := s.CreateMessage(mapi.PrivateFIDContacts, contactMsg("Edsger Dijkstra"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject(eid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenMessage(eid); err != ErrNotFound {
		t.Errorf("OpenMessage after delete: err %v, want ErrNotFound", err)
	}
	if objs, err := s.ListFolderObjects(mapi.PrivateFIDContacts); err != nil {
		t.Fatal(err)
	} else if len(objs) != 0 {
		t.Errorf("got %d objects after delete, want 0", len(objs))
	}
	if err := s.DeleteObject(eid); err != ErrNotFound {
		t.Errorf("second delete: err %v, want ErrNotFound", err)
	}
}

// TestFindObjectByProperty pins the lookup the delivery path uses to match an
// inbound meeting message to its appointment.
func TestFindObjectByProperty(t *testing.T) {
	s := openSeededStore(t)
	cal := int64(mapi.PrivateFIDCalendar)

	mk := func(uid string) int64 {
		var props mapi.PropertyValues
		props.Set(mapi.PrSubject, "appt "+uid)
		props.Set(mapi.PrInternetMessageID, uid)
		id, err := s.CreateMessage(cal, &oxcmail.Message{Props: props})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := mk("shared@hermex.test")
	mk("other@hermex.test")
	second := mk("shared@hermex.test")

	id, ok, err := s.FindObjectByProperty(cal, mapi.PrInternetMessageID, "other@hermex.test")
	if err != nil || !ok {
		t.Fatalf("lookup = ok %v err %v", ok, err)
	}
	if got := s.subjectOf(t, id); got != "appt other@hermex.test" {
		t.Errorf("matched the wrong object: %q", got)
	}

	if _, ok, err := s.FindObjectByProperty(cal, mapi.PrInternetMessageID, "absent@hermex.test"); err != nil || ok {
		t.Errorf("absent value = ok %v err %v, want not found", ok, err)
	}
	assertDuplicateResolvesToTheOldest(t, s, first, second)
}

// assertDuplicateResolvesToTheOldest pins the tie-break, which is what a scan in
// id order returned before the lookup moved into the store, and pins that a
// soft-deleted object leaves the live view.
func assertDuplicateResolvesToTheOldest(t *testing.T, s *Store, first, second int64) {
	t.Helper()
	cal := int64(mapi.PrivateFIDCalendar)
	id, ok, err := s.FindObjectByProperty(cal, mapi.PrInternetMessageID, "shared@hermex.test")
	if err != nil || !ok {
		t.Fatalf("duplicate lookup = ok %v err %v", ok, err)
	}
	if id != first {
		t.Errorf("duplicate resolved to %d, want the oldest (%d)", id, first)
	}
	if err := s.SoftDeleteObject(first); err != nil {
		t.Fatal(err)
	}
	id, ok, err = s.FindObjectByProperty(cal, mapi.PrInternetMessageID, "shared@hermex.test")
	if err != nil || !ok || id != second {
		t.Errorf("after soft-deleting the oldest, lookup = %d ok %v err %v, want %d", id, ok, err, second)
	}
}

// subjectOf reads one object's subject.
func (s *Store) subjectOf(t *testing.T, id int64) string {
	t.Helper()
	pv, err := s.GetMessageProperties(id, mapi.PrSubject)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := pv.Get(mapi.PrSubject)
	str, _ := v.(string)
	return str
}

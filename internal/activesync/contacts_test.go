package activesync

import (
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/wbxml"
)

// seedContact stores one IPM.Contact in the mailbox's Contacts folder, covering a
// direct scalar, a phone, a home-address field, a named email, and a date.
func seedContact(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameEmail1Address})
	if err != nil {
		t.Fatal(err)
	}
	props := mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
		{Tag: mapi.PrGivenName, Value: "Ada"},
		{Tag: mapi.PrSurname, Value: "Lovelace"},
		{Tag: mapi.PrCompanyName, Value: "Analytical Engine"},
		{Tag: mapi.PrMobileTelephoneNumber, Value: "+1-555-0100"},
		{Tag: mapi.PrHomeAddressCity, Value: "London"},
		{Tag: mapi.MakeTag(ids[0], mapi.PtUnicode), Value: "ada@analytical.test"},
		{Tag: mapi.PrBirthday, Value: mapi.UnixToNTTime(time.Date(1980, 12, 10, 0, 0, 0, 0, time.UTC))},
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDContacts), &oxcmail.Message{Props: props}); err != nil {
		t.Fatal(err)
	}
}

// contactsID is the Contacts collection id as the device addresses it.
func contactsID() string { return strconv.FormatInt(int64(mapi.PrivateFIDContacts), 10) }

// ctReq builds a single-collection Sync request for the Contacts folder.
func ctReq(key string, cmds ...*wbxml.Node) *wbxml.Node {
	coll := []*wbxml.Node{wbxml.Str(wbxml.ASSyncKey, key), wbxml.Str(wbxml.ASCollectionID, contactsID())}
	if len(cmds) > 0 {
		coll = append(coll, wbxml.Elem(wbxml.ASCommands, cmds...))
	}
	return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections, wbxml.Elem(wbxml.ASCollection, coll...)))
}

// TestContactAppData proves a stored contact's MAPI properties map to the
// MS-ASCONTACTS ApplicationData: scalars, the named email, a home-address field, and
// the birthday in the contact date-time format.
func TestContactAppData(t *testing.T) {
	_, dir := seededServer(t)
	seedContact(t, dir)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDContacts))
	if err != nil || len(objs) != 1 {
		t.Fatalf("want 1 contact, got %d (err %v)", len(objs), err)
	}
	data, err := contactAppData(st, objs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[wbxml.Tag]string{
		wbxml.CFirstName:         "Ada",
		wbxml.CLastName:          "Lovelace",
		wbxml.CCompanyName:       "Analytical Engine",
		wbxml.CMobilePhoneNumber: "+1-555-0100",
		wbxml.CHomeCity:          "London",
		wbxml.CEmail1Address:     "ada@analytical.test",
	}
	for tag, want := range checks {
		if got := data.ChildText(tag); got != want {
			t.Errorf("field %#x = %q, want %q", tag, got, want)
		}
	}
	if got := data.ChildText(wbxml.CBirthday); got != "1980-12-10T00:00:00.000Z" {
		t.Errorf("Birthday = %q, want 1980-12-10T00:00:00.000Z", got)
	}
}

// TestSyncContactsStreamsContact confirms the Contacts collection syncs its stored
// contacts as Adds carrying the contact fields.
func TestSyncContactsStreamsContact(t *testing.T) {
	ts, dir := seededServer(t)
	seedContact(t, dir)

	postCommand(t, ts, "Sync", ctReq("0"))
	_, root := postCommand(t, ts, "Sync", ctReq("1"))
	coll := respColl(t, root)
	if adds, _, _ := countCmds(coll); adds != 1 {
		t.Fatalf("got %d contact adds, want 1", adds)
	}
	data := coll.Child(wbxml.ASCommands).Children[0].Child(wbxml.ASData)
	if got := data.ChildText(wbxml.CFirstName); got != "Ada" {
		t.Errorf("FirstName = %q, want Ada", got)
	}
	if got := data.ChildText(wbxml.CEmail1Address); got != "ada@analytical.test" {
		t.Errorf("Email1Address = %q, want ada@analytical.test", got)
	}
}

// TestSyncContactsClientAdd confirms a device-created contact is stored with its MAPI
// properties (the same objects CardDAV reads) and not echoed back as a server add.
func TestSyncContactsClientAdd(t *testing.T) {
	ts, dir := seededServer(t)
	postCommand(t, ts, "Sync", ctReq("0"))
	add := wbxml.Elem(wbxml.ASAdd, wbxml.Str(wbxml.ASClientID, "cli-1"),
		wbxml.Elem(wbxml.ASData,
			wbxml.Str(wbxml.CFirstName, "Grace"),
			wbxml.Str(wbxml.CLastName, "Hopper"),
			wbxml.Str(wbxml.CEmail1Address, "grace@navy.test")))
	_, root := postCommand(t, ts, "Sync", ctReq("1", add))
	coll := respColl(t, root)

	addResp := coll.Child(wbxml.ASResponses).Child(wbxml.ASAdd)
	if addResp == nil || addResp.ChildText(wbxml.ASClientID) != "cli-1" {
		t.Fatalf("no Add response for the client contact: %+v", addResp)
	}
	if adds, _, _ := countCmds(coll); adds != 0 {
		t.Errorf("the client's add was echoed back as a server add (%d)", adds)
	}
	sid := addResp.ChildText(wbxml.ASServerID)
	id, err := strconv.ParseInt(sid, 10, 64)
	if err != nil {
		t.Fatalf("bad server id %q", sid)
	}

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameEmail1Address})
	if err != nil {
		t.Fatal(err)
	}
	pv, err := st.GetMessageProperties(id, mapi.PrGivenName, mapi.PrSurname, mapi.MakeTag(ids[0], mapi.PtUnicode))
	if err != nil {
		t.Fatal(err)
	}
	if got := stringProp(pv, mapi.PrGivenName); got != "Grace" {
		t.Errorf("stored GivenName = %q, want Grace", got)
	}
	if got := stringProp(pv, mapi.MakeTag(ids[0], mapi.PtUnicode)); got != "grace@navy.test" {
		t.Errorf("stored Email1 = %q, want grace@navy.test", got)
	}
}

// TestFolderSyncAdvertisesContacts confirms FolderSync exposes the Contacts
// collection with EAS folder type 9.
func TestFolderSyncAdvertisesContacts(t *testing.T) {
	ts, _ := seededServer(t)
	_, root := postCommand(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	changes := root.Child(wbxml.FHChanges)
	if changes == nil {
		t.Fatal("FolderSync returned no Changes")
	}
	for _, add := range changes.Children {
		if add.Tag == wbxml.FHAdd && add.ChildText(wbxml.FHServerID) == contactsID() {
			if got := add.ChildText(wbxml.FHType); got != "9" {
				t.Errorf("Contacts folder Type = %q, want 9", got)
			}
			return
		}
	}
	t.Error("FolderSync did not advertise the Contacts collection")
}

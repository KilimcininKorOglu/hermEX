package dav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// hobbies is PidTagHobbies (MS-OXPROPS), a property no DAV format carries,
// standing in for what another client stored.
const hobbies = mapi.PropTag(0x3A43001F)

// inPlaceServer starts a DAV server and returns the mailbox directory it serves.
func inPlaceServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	accs := seedCalendar(t)
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, accs[testUser].MailboxPath
}

// openInPlace opens the served mailbox for a direct look, closed with the test.
func openInPlace(t *testing.T, dir string) *objectstore.Store {
	t.Helper()
	st, err := objectstore.Open(dir)
	must(t, err, "open mailbox")
	t.Cleanup(func() { st.Close() })
	return st
}

func must(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// putETag stores body at url and returns the ETag the PUT answered with.
func putETag(t *testing.T, ts *httptest.Server, url, body string, want int) string {
	t.Helper()
	resp, out := doFull(t, ts, "PUT", url, body, nil)
	if resp.StatusCode != want {
		t.Fatalf("PUT %s: status %d, want %d\n%s", url, resp.StatusCode, want, out)
	}
	return resp.Header.Get("ETag")
}

// objectIDOf resolves the stored object a resource name addresses.
func objectIDOf(t *testing.T, st *objectstore.Store, fid int64, suffix, name string) int64 {
	t.Helper()
	obj, found, err := findObjectByName(st, fid, suffix, name)
	must(t, err, "find object")
	if !found {
		t.Fatalf("no object named %s", name)
	}
	return obj.ID
}

// wantSameObject holds that a replacing PUT kept the object, its foreign
// property and its ETag moving, and left nothing in the recoverable items.
func wantSameObject(t *testing.T, st *objectstore.Store, before, after int64, oldTag, newTag string) {
	t.Helper()
	if after != before {
		t.Errorf("object id changed from %d to %d", before, after)
	}
	pv, err := st.GetMessageProperties(after, hobbies)
	must(t, err, "read foreign property")
	if v, _ := pv.Get(hobbies); v != "chess" {
		t.Errorf("foreign property = %v, want chess", v)
	}
	if oldTag == newTag {
		t.Errorf("ETag stayed %s across the replace", newTag)
	}
	deleted, err := st.ListAllSoftDeleted()
	must(t, err, "list recoverable")
	if len(deleted) != 0 {
		t.Errorf("replace left %d recoverable items, want 0", len(deleted))
	}
}

// hasTag reports whether a stored object carries tag.
func hasTag(t *testing.T, st *objectstore.Store, id int64, tag mapi.PropTag) bool {
	t.Helper()
	pv, err := st.GetMessageProperties(id, tag)
	must(t, err, "read property")
	return pv.Has(tag)
}

// namedTagOf resolves a named property the store already allocated.
func namedTagOf(t *testing.T, st *objectstore.Store, name mapi.PropertyName, typ mapi.PropType) mapi.PropTag {
	t.Helper()
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{name})
	must(t, err, "resolve named property")
	return mapi.MakeTag(ids[0], typ)
}

// TestContactPutKeepsTheSameObject replaces a contact whose picture webmail set:
// a card without a PHOTO keeps the picture and the foreign property, and a card
// with one swaps the picture for it.
func TestContactPutKeepsTheSameObject(t *testing.T) {
	ts, dir := inPlaceServer(t)
	url := contactURL("ada.vcf")
	first := putETag(t, ts, url, adaVCard, http.StatusCreated)
	st := openInPlace(t, dir)
	id := objectIDOf(t, st, int64(mapi.PrivateFIDContacts), ".vcf", "ada.vcf")
	must(t, st.SetMessageProperties(id, mapi.PropertyValues{{Tag: hobbies, Value: "chess"}}), "set foreign property")
	_, _, err := st.CreateAttachment(id, mapi.PropertyValues{
		{Tag: mapi.PrAttachDataBin, Value: []byte("old")}, {Tag: mapi.PrAttachmentContactPhoto, Value: true}})
	must(t, err, "attach picture")
	picture := namedTagOf(t, st, mapi.NameHasPicture, mapi.PtBoolean)
	must(t, st.SetMessageProperties(id, mapi.PropertyValues{{Tag: picture, Value: true}}), "set has-picture")

	second := putETag(t, ts, url, strings.Replace(adaVCard, "Ada Lovelace", "Ada A. Lovelace", 1), http.StatusNoContent)
	wantSameObject(t, st, id, objectIDOf(t, st, int64(mapi.PrivateFIDContacts), ".vcf", "ada.vcf"), first, second)
	wantPicture(t, st, id, "old")
	if !hasTag(t, st, id, picture) {
		t.Error("a card without a PHOTO cleared the has-picture flag")
	}

	withPhoto := strings.Replace(adaVCard, "UID:", "PHOTO:data:image/png;base64,bmV3\r\nUID:", 1)
	putETag(t, ts, url, withPhoto, http.StatusNoContent)
	wantPicture(t, st, id, "new")
}

// wantPicture holds a contact's single picture attachment to the given bytes.
func wantPicture(t *testing.T, st *objectstore.Store, id int64, want string) {
	t.Helper()
	msg, err := st.OpenMessage(id)
	must(t, err, "open contact")
	if len(msg.Attachments) != 1 {
		t.Fatalf("contact has %d attachments, want 1", len(msg.Attachments))
	}
	_, data, ok := objectstore.ContactPhotoAttachment(msg)
	if !ok || string(data) != want {
		t.Errorf("picture = %q (found %v), want %q", data, ok, want)
	}
}

// meetingICS is a meeting with Bob, plus extra lines inside the VEVENT.
func meetingICS(extra string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:meet-1\r\nSUMMARY:Review\r\n" +
		"DTSTART:20260615T140000Z\r\nDTEND:20260615T150000Z\r\nORGANIZER:mailto:" + testUser + "\r\n" +
		"ATTENDEE:mailto:bob@example.test\r\n" + extra + "END:VEVENT\r\nEND:VCALENDAR\r\n"
}

// TestEventPutKeepsTheSameObject replaces a meeting Bob accepted: the id, the
// foreign property and Bob's response stay, Carol joins, the dropped LOCATION
// goes.
func TestEventPutKeepsTheSameObject(t *testing.T) {
	ts, dir := inPlaceServer(t)
	url := calURL("meet.ics")
	first := putETag(t, ts, url, meetingICS("LOCATION:Room 1\r\n"), http.StatusCreated)
	st := openInPlace(t, dir)
	id := objectIDOf(t, st, int64(mapi.PrivateFIDCalendar), ".ics", "meet.ics")
	must(t, st.SetMessageProperties(id, mapi.PropertyValues{{Tag: hobbies, Value: "chess"}}), "set foreign property")
	resp := namedTagOf(t, st, mapi.NameResponseStatus, mapi.PtLong)
	recips, err := st.ListRecipients(id)
	must(t, err, "list recipients")
	must(t, st.SetRecipientProperties(recips[0].ID, mapi.PropertyValues{{Tag: resp, Value: int32(3)}}), "accept")

	second := putETag(t, ts, url, meetingICS("ATTENDEE:mailto:carol@example.test\r\n"), http.StatusNoContent)
	wantSameObject(t, st, id, objectIDOf(t, st, int64(mapi.PrivateFIDCalendar), ".ics", "meet.ics"), first, second)
	if hasTag(t, st, id, namedTagOf(t, st, mapi.NameAppointmentLocation, mapi.PtUnicode)) {
		t.Error("the dropped LOCATION is still stored")
	}
	recips, err = st.ListRecipients(id)
	must(t, err, "list recipients after")
	if len(recips) != 2 {
		t.Fatalf("meeting has %d attendees, want 2", len(recips))
	}
	for _, r := range recips {
		pv, err := st.GetRecipientProperties(r.ID, resp)
		must(t, err, "read response")
		if v, _ := pv.Get(resp); r.SmtpAddress == "bob@example.test" && v != int32(3) {
			t.Errorf("Bob's response = %v, want 3", v)
		}
	}
}

// TestTaskPutKeepsTheSameObject replaces a VTODO: the id and the foreign
// property stay, the dropped DUE goes.
func TestTaskPutKeepsTheSameObject(t *testing.T) {
	ts, dir := inPlaceServer(t)
	url := "/dav/calendars/" + testUser + "/tasks/t1.ics"
	vtodo := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\nUID:t1\r\nSUMMARY:Buy milk\r\n" +
		"DUE:20260701T170000Z\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	first := putETag(t, ts, url, vtodo, http.StatusCreated)
	st := openInPlace(t, dir)
	id := objectIDOf(t, st, int64(mapi.PrivateFIDTasks), ".ics", "t1.ics")
	must(t, st.SetMessageProperties(id, mapi.PropertyValues{{Tag: hobbies, Value: "chess"}}), "set foreign property")

	second := putETag(t, ts, url, strings.Replace(vtodo, "DUE:20260701T170000Z\r\n", "", 1), http.StatusNoContent)
	wantSameObject(t, st, id, objectIDOf(t, st, int64(mapi.PrivateFIDTasks), ".ics", "t1.ics"), first, second)
	if hasTag(t, st, id, namedTagOf(t, st, mapi.NameTaskDueDate, mapi.PtSysTime)) {
		t.Error("the dropped DUE is still stored")
	}
}

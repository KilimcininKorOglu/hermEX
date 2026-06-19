package activesync

import (
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/wbxml"
)

// TestCalendarAppData proves a stored appointment's calendar named properties map
// to the MS-ASCAL ApplicationData: subject/location/busy-status verbatim, the
// all-day flag as 0/1, and start/end as UTC compact times under a timezone.
func TestCalendarAppData(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole,
		mapi.NameAppointmentEndWhole,
		mapi.NameBusyStatus,
		mapi.NameAppointmentLocation,
		mapi.NameAppointmentSubType,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 19, 9, 30, 0, 0, time.UTC)
	props := mapi.PropertyValues{
		{Tag: mapi.MakeTag(ids[0], mapi.PtSysTime), Value: mapi.UnixToNTTime(start)},
		{Tag: mapi.MakeTag(ids[1], mapi.PtSysTime), Value: mapi.UnixToNTTime(end)},
		{Tag: mapi.MakeTag(ids[2], mapi.PtLong), Value: int32(2)}, // busy
		{Tag: mapi.MakeTag(ids[3], mapi.PtUnicode), Value: "Room 1"},
		{Tag: mapi.MakeTag(ids[4], mapi.PtBoolean), Value: false},
		{Tag: mapi.PrSubject, Value: "Standup"},
	}
	id, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props})
	if err != nil {
		t.Fatal(err)
	}

	data, err := calendarAppData(st, id)
	if err != nil {
		t.Fatal(err)
	}
	if data == nil {
		t.Fatal("calendarAppData returned nil for a stored appointment")
	}
	for tag, want := range map[wbxml.Tag]string{
		wbxml.CalSubject:       "Standup",
		wbxml.CalStartTime:     "20260619T090000Z",
		wbxml.CalEndTime:       "20260619T093000Z",
		wbxml.CalBusyStatus:    "2",
		wbxml.CalAllDayEvent:   "0",
		wbxml.CalLocation:      "Room 1",
		wbxml.CalMeetingStatus: "0",
	} {
		if got := data.ChildText(tag); got != want {
			t.Errorf("ChildText(%#06x) = %q, want %q", uint16(tag), got, want)
		}
	}
	if data.ChildText(wbxml.CalTimezone) == "" {
		t.Error("CalTimezone is empty; appointment times need a timezone")
	}
}

// TestSyncCalendarStreamsAppointment proves the Sync command serves the Calendar
// collection: priming returns nothing, then the first real sync streams the stored
// appointment as an Add carrying MS-ASCAL ApplicationData.
func TestSyncCalendarStreamsAppointment(t *testing.T) {
	ts, dir := seededServer(t)
	seedAppointment(t, dir, "Standup",
		time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 19, 9, 30, 0, 0, time.UTC))

	calID := strconv.FormatInt(int64(mapi.PrivateFIDCalendar), 10)
	calReq := func(key string) *wbxml.Node {
		return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections,
			wbxml.Elem(wbxml.ASCollection,
				wbxml.Str(wbxml.ASSyncKey, key),
				wbxml.Str(wbxml.ASCollectionID, calID))))
	}

	_, root := postCommand(t, ts, "Sync", calReq("0"))
	if respColl(t, root).Child(wbxml.ASCommands) != nil {
		t.Error("calendar prime must not return items")
	}

	_, root = postCommand(t, ts, "Sync", calReq("1"))
	coll := respColl(t, root)
	if adds, _, _ := countCmds(coll); adds != 1 {
		t.Fatalf("got %d calendar adds, want 1", adds)
	}
	data := coll.Child(wbxml.ASCommands).Children[0].Child(wbxml.ASData)
	if got := data.ChildText(wbxml.CalSubject); got != "Standup" {
		t.Errorf("CalSubject = %q, want Standup", got)
	}
	if got := data.ChildText(wbxml.CalStartTime); got != "20260619T090000Z" {
		t.Errorf("CalStartTime = %q, want the UTC compact time", got)
	}
}

// TestSyncCalendarClientEdits proves the calendar Sync path applies a device's
// Change (persisted, and not echoed back to the device that made it) and Delete.
func TestSyncCalendarClientEdits(t *testing.T) {
	ts, dir := seededServer(t)
	seedAppointment(t, dir, "Standup",
		time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 19, 9, 30, 0, 0, time.UTC))
	calID := strconv.FormatInt(int64(mapi.PrivateFIDCalendar), 10)
	calReq := func(key string, cmds ...*wbxml.Node) *wbxml.Node {
		coll := []*wbxml.Node{wbxml.Str(wbxml.ASSyncKey, key), wbxml.Str(wbxml.ASCollectionID, calID)}
		if len(cmds) > 0 {
			coll = append(coll, wbxml.Elem(wbxml.ASCommands, cmds...))
		}
		return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections, wbxml.Elem(wbxml.ASCollection, coll...)))
	}

	postCommand(t, ts, "Sync", calReq("0"))
	_, root := postCommand(t, ts, "Sync", calReq("1"))
	coll := respColl(t, root)
	sid := coll.Child(wbxml.ASCommands).Children[0].ChildText(wbxml.ASServerID)
	id, err := strconv.ParseInt(sid, 10, 64)
	if err != nil {
		t.Fatalf("bad server id %q", sid)
	}

	// Client Change: rename the appointment.
	change := wbxml.Elem(wbxml.ASChange, wbxml.Str(wbxml.ASServerID, sid),
		wbxml.Elem(wbxml.ASData, wbxml.Str(wbxml.CalSubject, "Renamed")))
	_, root = postCommand(t, ts, "Sync", calReq(coll.ChildText(wbxml.ASSyncKey), change))
	if adds, changes, _ := countCmds(respColl(t, root)); adds+changes != 0 {
		t.Errorf("the client's change was echoed back: adds=%d changes=%d", adds, changes)
	}
	if got := storedSubject(t, dir, id); got != "Renamed" {
		t.Errorf("stored subject = %q, want the client's edit Renamed", got)
	}

	// Client Delete: the appointment is removed.
	del := wbxml.Elem(wbxml.ASDelete, wbxml.Str(wbxml.ASServerID, sid))
	postCommand(t, ts, "Sync", calReq(respColl(t, root).ChildText(wbxml.ASSyncKey), del))
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar)); err != nil || len(objs) != 0 {
		t.Errorf("calendar still has %d object(s) after the client delete (err %v)", len(objs), err)
	}
}

// TestSyncCalendarClientAdd proves a device-created appointment is stored and
// answered with a client-id -> server-id mapping in the Responses section, and is
// not echoed back to the device that created it.
func TestSyncCalendarClientAdd(t *testing.T) {
	ts, dir := seededServer(t)
	calID := strconv.FormatInt(int64(mapi.PrivateFIDCalendar), 10)
	calReq := func(key string, cmds ...*wbxml.Node) *wbxml.Node {
		coll := []*wbxml.Node{wbxml.Str(wbxml.ASSyncKey, key), wbxml.Str(wbxml.ASCollectionID, calID)}
		if len(cmds) > 0 {
			coll = append(coll, wbxml.Elem(wbxml.ASCommands, cmds...))
		}
		return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections, wbxml.Elem(wbxml.ASCollection, coll...)))
	}

	postCommand(t, ts, "Sync", calReq("0"))
	add := wbxml.Elem(wbxml.ASAdd, wbxml.Str(wbxml.ASClientID, "cli-1"),
		wbxml.Elem(wbxml.ASData,
			wbxml.Str(wbxml.CalSubject, "From Phone"),
			wbxml.Str(wbxml.CalStartTime, "20260620T140000Z"),
			wbxml.Str(wbxml.CalEndTime, "20260620T150000Z")))
	_, root := postCommand(t, ts, "Sync", calReq("1", add))
	coll := respColl(t, root)

	resp := coll.Child(wbxml.ASResponses)
	if resp == nil {
		t.Fatal("no Responses section for the client add")
	}
	addResp := resp.Child(wbxml.ASAdd)
	if addResp == nil {
		t.Fatal("no Add response")
	}
	if got := addResp.ChildText(wbxml.ASClientID); got != "cli-1" {
		t.Errorf("response ClientId = %q, want cli-1", got)
	}
	sid := addResp.ChildText(wbxml.ASServerID)
	if sid == "" {
		t.Fatal("Add response has no ServerId")
	}
	if adds, _, _ := countCmds(coll); adds != 0 {
		t.Errorf("the client's add was echoed back as a server add (%d)", adds)
	}
	id, err := strconv.ParseInt(sid, 10, 64)
	if err != nil {
		t.Fatalf("bad server id %q", sid)
	}
	if got := storedSubject(t, dir, id); got != "From Phone" {
		t.Errorf("stored subject = %q, want From Phone", got)
	}
}

// storedSubject reads a stored object's PR_SUBJECT.
func storedSubject(t *testing.T, dir string, id int64) string {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pv, err := st.GetMessageProperties(id, mapi.PrSubject)
	if err != nil {
		t.Fatal(err)
	}
	return stringProp(pv, mapi.PrSubject)
}

// seedAppointment stores one appointment in the mailbox's Calendar folder.
func seedAppointment(t *testing.T, dir, subject string, start, end time.Time) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole,
		mapi.NameAppointmentEndWhole,
		mapi.NameBusyStatus,
		mapi.NameAppointmentSubType,
	})
	if err != nil {
		t.Fatal(err)
	}
	props := mapi.PropertyValues{
		{Tag: mapi.MakeTag(ids[0], mapi.PtSysTime), Value: mapi.UnixToNTTime(start)},
		{Tag: mapi.MakeTag(ids[1], mapi.PtSysTime), Value: mapi.UnixToNTTime(end)},
		{Tag: mapi.MakeTag(ids[2], mapi.PtLong), Value: int32(2)},
		{Tag: mapi.MakeTag(ids[3], mapi.PtBoolean), Value: false},
		{Tag: mapi.PrSubject, Value: subject},
	}
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props}); err != nil {
		t.Fatal(err)
	}
}

// TestCalendarAppDataRecurring proves a recurring appointment — which stores only
// its start named property plus the verbatim iCal, no end — is served with its end
// and an MS-ASCAL Recurrence subtree parsed from that iCal, rather than skipped.
func TestCalendarAppDataRecurring(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ical := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:rec-1\r\nDTSTART:20260601T090000Z\r\n" +
		"DTEND:20260601T093000Z\r\nSUMMARY:Weekly Sync\r\nRRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=5\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n")
	msg, err := oxcical.Import(ical, oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), msg)
	if err != nil {
		t.Fatal(err)
	}

	data, err := calendarAppData(st, id)
	if err != nil {
		t.Fatal(err)
	}
	if data == nil {
		t.Fatal("recurring appointment was skipped (calendarAppData returned nil)")
	}
	if got := data.ChildText(wbxml.CalStartTime); got != "20260601T090000Z" {
		t.Errorf("CalStartTime = %q", got)
	}
	if got := data.ChildText(wbxml.CalEndTime); got != "20260601T093000Z" {
		t.Errorf("CalEndTime = %q (end must come from the iCal for a recurring event)", got)
	}
	rec := data.Child(wbxml.CalRecurrence)
	if rec == nil {
		t.Fatal("recurring appointment has no Recurrence element")
	}
	if got := rec.ChildText(wbxml.CalType); got != "1" {
		t.Errorf("Recurrence Type = %q, want 1 (weekly)", got)
	}
	if got := rec.ChildText(wbxml.CalDayOfWeek); got != "2" {
		t.Errorf("Recurrence DayOfWeek = %q, want 2 (Monday)", got)
	}
	if got := rec.ChildText(wbxml.CalOccurrences); got != "5" {
		t.Errorf("Recurrence Occurrences = %q, want 5", got)
	}
}

// TestCalendarAppDataNoAppointment proves a calendar folder that has never stored
// an appointment (no start/end named id) yields no application data rather than an
// error, so the Sync path can skip it cleanly.
func TestCalendarAppDataNoAppointment(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	data, err := calendarAppData(st, 1)
	if err != nil {
		t.Fatalf("calendarAppData on a bare store: %v", err)
	}
	if data != nil {
		t.Errorf("expected nil application data when no appointment props exist, got %#v", data)
	}
}

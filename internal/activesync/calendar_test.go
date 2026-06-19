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

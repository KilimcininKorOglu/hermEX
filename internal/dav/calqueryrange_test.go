package dav

import (
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// seedRangeEvent stores one appointment carrying the two time named properties.
func seedRangeEvent(t *testing.T, st *objectstore.Store, start, end time.Time) int64 {
	t.Helper()
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole,
	})
	if err != nil {
		t.Fatal(err)
	}
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.Appointment")
	props.Set(mapi.MakeTag(ids[0], mapi.PtSysTime), mapi.UnixToNTTime(start))
	props.Set(mapi.MakeTag(ids[1], mapi.PtSysTime), mapi.UnixToNTTime(end))
	id, err := st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// eventRangeFilter builds the VCALENDAR > VEVENT[time-range] filter every calendar
// client sends for a view.
func eventRangeFilter(start, end string) *filter {
	return &filter{CompFilters: []compFilter{{
		Name: "VCALENDAR",
		CompFilters: []compFilter{{
			Name:      "VEVENT",
			TimeRange: &timeRange{Start: start, End: end},
		}},
	}}}
}

// TestCalQueryObjectsAppliesTheRange proves the calendar-query range reaches the
// store. Without it the handler builds calendar-data for every member of the
// folder and then throws away the ones outside the range, so the work grows with
// the calendar rather than with the answer.
func TestCalQueryObjectsAppliesTheRange(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fid := int64(mapi.PrivateFIDCalendar)

	june := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	inRange := seedRangeEvent(t, st, june, june.Add(time.Hour))
	seedRangeEvent(t, st, june.AddDate(0, -3, 0), june.AddDate(0, -3, 0).Add(time.Hour))
	seedRangeEvent(t, st, june.AddDate(0, 3, 0), june.AddDate(0, 3, 0).Add(time.Hour))

	filt := eventRangeFilter("20260601T000000Z", "20260701T000000Z")
	got, err := calQueryObjects(st, fid, false, filt)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != inRange {
		t.Fatalf("range query returned %d objects (%+v), want just %d", len(got), got, inRange)
	}
}

// TestCalQueryObjectsFallsBackOnOtherShapes keeps every filter the pre-filter
// cannot reason about on the full listing. Narrowing one of these in the store
// would drop members the filter would have matched.
func TestCalQueryObjectsFallsBackOnOtherShapes(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fid := int64(mapi.PrivateFIDCalendar)

	june := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	for i := range 3 {
		seedRangeEvent(t, st, june.AddDate(0, 4*i-4, 0), june.AddDate(0, 4*i-4, 0).Add(time.Hour))
	}

	multi := eventRangeFilter("20260601T000000Z", "20260701T000000Z")
	multi.CompFilters[0].CompFilters = append(multi.CompFilters[0].CompFilters, compFilter{Name: "VTODO"})

	notDefined := eventRangeFilter("20260601T000000Z", "20260701T000000Z")
	notDefined.CompFilters[0].CompFilters[0].IsNotDefined = &struct{}{}

	unparseable := eventRangeFilter("not-a-time", "20260701T000000Z")

	noRange := eventRangeFilter("20260601T000000Z", "20260701T000000Z")
	noRange.CompFilters[0].CompFilters[0].TimeRange = nil

	for _, c := range []struct {
		name string
		sync bool
		filt *filter
	}{
		{"nil filter", false, nil},
		{"sync-collection", true, eventRangeFilter("20260601T000000Z", "20260701T000000Z")},
		{"more than one component", false, multi},
		{"is-not-defined", false, notDefined},
		{"unparseable bound", false, unparseable},
		{"no time-range", false, noRange},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := calQueryObjects(st, fid, c.sync, c.filt)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 3 {
				t.Errorf("returned %d objects, want all 3", len(got))
			}
		})
	}
}

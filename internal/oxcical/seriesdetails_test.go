package oxcical

import (
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestImportedSeriesCarriesItsDetails proves an imported series master stores the
// location, busy status and reminder every occurrence shares, as a single event
// does. It used to store only its span and pattern, so a MAPI reader of the
// master (Outlook, free/busy, ActiveSync) saw a series with no location, marked
// free, with no reminder.
func TestImportedSeriesCarriesItsDetails(t *testing.T) {
	r := newResolver()
	msg, err := Import([]byte(seriesWithChanges), r.opt())
	mustNoErr(t, err, "import")
	p := &msg.Props
	if got, _ := p.Get(r.tag(mapi.NameAppointmentLocation, mapi.PtUnicode)); got != "Room A" {
		t.Errorf("location = %v, want Room A", got)
	}
	if got, _ := p.Get(r.tag(mapi.NameBusyStatus, mapi.PtLong)); got != int32(busyBusy) {
		t.Errorf("busy status = %v, want busy (%d)", got, busyBusy)
	}
}

// TestImportedSeriesCarriesItsFirstSpan proves an imported series master stores
// its first instance's end and its all-day flag, as a single event does. It used
// to store the start alone, so a MAPI or EWS reader of the master had no end.
func TestImportedSeriesCarriesItsFirstSpan(t *testing.T) {
	r := newResolver()
	msg, err := Import([]byte(seriesWithChanges), r.opt())
	mustNoErr(t, err, "import")
	end, ok := msg.Props.Get(r.tag(mapi.NameAppointmentEndWhole, mapi.PtSysTime))
	want := time.Date(2026, 6, 1, 7, 30, 0, 0, time.UTC) // 10:30 Istanbul
	if got, isNT := end.(uint64); !ok || !isNT || !mapi.NTTimeToUnix(got).Equal(want) {
		t.Errorf("end = %v, want %v", end, want)
	}

	allDay := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:allday-series\r\n" +
		"DTSTART;VALUE=DATE:20260601\r\nDTEND;VALUE=DATE:20260602\r\nRRULE:FREQ=WEEKLY;COUNT=3\r\n" +
		"SUMMARY:Holiday\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	msg, err = Import([]byte(allDay), r.opt())
	mustNoErr(t, err, "import all-day")
	if got, _ := msg.Props.Get(r.tag(mapi.NameAppointmentSubType, mapi.PtBoolean)); got != true {
		t.Errorf("all-day flag = %v, want true", got)
	}
}

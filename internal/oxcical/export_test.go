package oxcical

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// exportWithBusyStatus exports one appointment carrying the given busy status.
func exportWithBusyStatus(t *testing.T, status int32) string {
	t.Helper()
	r := newResolver()
	_, _ = r.resolve(true, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole, mapi.NameAppointmentEndWhole, mapi.NameBusyStatus,
	})
	start := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	msg := &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Appointment"},
		{Tag: mapi.PrSubject, Value: "Home office"},
		{Tag: r.tag(mapi.NameAppointmentStartWhole, mapi.PtSysTime), Value: mapi.UnixToNTTime(start)},
		{Tag: r.tag(mapi.NameAppointmentEndWhole, mapi.PtSysTime), Value: mapi.UnixToNTTime(start.Add(8 * time.Hour))},
		{Tag: r.tag(mapi.NameBusyStatus, mapi.PtLong), Value: status},
	}}
	out, err := Export(msg, r.opt())
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestExportTransparencyFollowsOccupancy pins what a CalDAV client aggregates in
// a VFREEBUSY. TRANSP says whether the event takes the attendee's time, so a
// home office day (working elsewhere) must export transparent or it blocks the
// whole day for every CalDAV client.
func TestExportTransparencyFollowsOccupancy(t *testing.T) {
	for _, c := range []struct {
		status int32
		want   string
	}{
		{mapi.BusyFree, "TRANSP:TRANSPARENT"},
		{mapi.BusyTentative, "TRANSP:OPAQUE"},
		{mapi.BusyBusy, "TRANSP:OPAQUE"},
		{mapi.BusyOOF, "TRANSP:OPAQUE"},
		{mapi.BusyWorkingElsewhere, "TRANSP:TRANSPARENT"},
	} {
		ics := exportWithBusyStatus(t, c.status)
		if !strings.Contains(ics, c.want) {
			t.Errorf("status %d exported without %q:\n%s", c.status, c.want, ics)
		}
	}
}

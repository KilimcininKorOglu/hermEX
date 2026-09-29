package oxcical

import (
	"testing"

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

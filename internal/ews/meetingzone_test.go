package ews

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// setZoneDescription stores PidLidTimeZoneDescription on the seeded invitation.
func setZoneDescription(t *testing.T, dir, itemID, desc string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := oxews.DecodeItemID(itemID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameTimeZoneDescription})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageProperties(id.MessageID, mapi.PropertyValues{{Tag: mapi.MakeTag(ids[0], mapi.PtUnicode), Value: desc}}); err != nil {
		t.Fatal(err)
	}
}

// TestMeetingRequestNamesItsZone proves an invitation whose time zone description
// names a zone carries it as StartTimeZone and EndTimeZone by its Windows id, the
// zone the iCalendar export reads too, and that an empty description is no zone:
// neither element is written, rather than one with an empty Id.
func TestMeetingRequestNamesItsZone(t *testing.T) {
	for desc, want := range map[string]string{
		"Europe/Berlin": `<StartTimeZone Id="W. Europe Standard Time"></StartTimeZone><EndTimeZone Id="W. Europe Standard Time"></EndTimeZone>`,
		"":              "",
	} {
		dir, itemID := seedMeetingRequest(t)
		setZoneDescription(t, dir, itemID, desc)
		_, out := soapPost(t, meetingServer(t, dir), getItemReq(itemID), true)
		if want == "" {
			if strings.Contains(out, "TimeZone") {
				t.Errorf("an empty description produced a zone:\n%s", out)
			}
			continue
		}
		if !strings.Contains(out, "</Organizer>"+want) {
			t.Errorf("%q: want %s after Organizer:\n%s", desc, want, out)
		}
	}
}

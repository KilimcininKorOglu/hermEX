package oxcical

import (
	"slices"
	"testing"

	"hermex/internal/mapi"
)

// managedFixtures together exercise every property Import can write: a zoned
// meeting request with every synthesized field, an all-day event, and a zoned
// series master.
var managedFixtures = []string{
	"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:m-1\r\nSUMMARY:Review\r\n" +
		"DESCRIPTION:Agenda\r\nLOCATION:Room 1\r\nCLASS:PRIVATE\r\nPRIORITY:1\r\nSEQUENCE:2\r\n" +
		"DTSTART;TZID=Europe/Berlin:20260612T090000\r\nDTEND;TZID=Europe/Berlin:20260612T100000\r\n" +
		"ORGANIZER;CN=Alice:mailto:alice@example.test\r\nATTENDEE;CN=Bob:mailto:bob@example.test\r\n" +
		"BEGIN:VALARM\r\nTRIGGER:-PT15M\r\nACTION:DISPLAY\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:m-2\r\nSUMMARY:Holiday\r\n" +
		"DTSTART;VALUE=DATE:20260612\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:m-3\r\nSUMMARY:Standup\r\n" +
		"DTSTART;TZID=Europe/Berlin:20260612T090000\r\nDTEND;TZID=Europe/Berlin:20260612T093000\r\n" +
		"RRULE:FREQ=DAILY\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
}

// TestManagedTagsAreWhatImportWrites holds ManagedTags equal to the tags the
// fixtures produce. A field added to Import without a place in the set leaves a
// stale value behind on every edit that drops it; a tag in the set that Import
// never writes deletes what another client stored.
func TestManagedTagsAreWhatImportWrites(t *testing.T) {
	r := newResolver()
	written := map[mapi.PropTag]bool{}
	for _, body := range managedFixtures {
		msg, err := Import([]byte(body), r.opt())
		mustNoErr(t, err, "import")
		for _, pv := range msg.Props {
			written[pv.Tag] = true
		}
	}
	managed, err := ManagedTags(r.opt())
	mustNoErr(t, err, "managed tags")
	for tag := range written {
		if !slices.Contains(managed, tag) {
			t.Errorf("Import writes %#x, which ManagedTags leaves out", uint32(tag))
		}
	}
	for _, tag := range managed {
		if !written[tag] {
			t.Errorf("ManagedTags holds %#x, which no fixture writes", uint32(tag))
		}
	}
}

// TestManagedTagsAllocateNothing keeps the lookup read-only: asking which tags are
// managed must not allocate named properties in the store.
func TestManagedTagsAllocateNothing(t *testing.T) {
	r := newResolver()
	managed, err := ManagedTags(r.opt())
	mustNoErr(t, err, "managed tags")
	if len(r.ids) != 0 {
		t.Errorf("ManagedTags allocated %d named properties, want 0", len(r.ids))
	}
	if len(managed) != len(managedFixed) {
		t.Errorf("fresh store has %d managed tags, want the %d fixed ones", len(managed), len(managedFixed))
	}
}

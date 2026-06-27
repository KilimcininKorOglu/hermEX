package oxcical

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestVJournalRoundTrip confirms a VJOURNAL imports to an IPM.Activity message and
// exports back with its SUMMARY/DESCRIPTION/DTSTART intact (served verbatim).
func TestVJournalRoundTrip(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VJOURNAL\r\nUID:j-1\r\nSUMMARY:Trip log\r\n" +
		"DESCRIPTION:Visited the lab\r\nDTSTART:20260701T090000Z\r\nEND:VJOURNAL\r\nEND:VCALENDAR\r\n"
	msg, err := ImportVJournal([]byte(raw), newResolver().opt())
	if err != nil {
		t.Fatalf("ImportVJournal: %v", err)
	}
	if v, _ := msg.Props.Get(mapi.PrMessageClass); v != "IPM.Activity" {
		t.Errorf("message class = %v, want IPM.Activity", v)
	}
	if v, _ := msg.Props.Get(mapi.PrSubject); v != "Trip log" {
		t.Errorf("PrSubject = %v, want the SUMMARY", v)
	}
	out := string(ExportVJournal(msg, "j-1"))
	for _, want := range []string{"BEGIN:VJOURNAL", "SUMMARY:Trip log", "DESCRIPTION:Visited the lab", "DTSTART:20260701T090000Z", "END:VJOURNAL"} {
		if !strings.Contains(out, want) {
			t.Errorf("round-trip lost %q\n%s", want, out)
		}
	}
}

// TestImportVJournalRejectsNonJournal confirms a non-journal object is rejected.
func TestImportVJournalRejectsNonJournal(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if _, err := ImportVJournal([]byte(raw), newResolver().opt()); err == nil {
		t.Error("ImportVJournal should reject a calendar with no VJOURNAL")
	}
}

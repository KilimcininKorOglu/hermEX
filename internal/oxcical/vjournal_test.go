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
	if v, ok := msg.Props.Get(mapi.PrMessageClass); !ok || v != "IPM.Activity" {
		t.Errorf("message class = %v (ok=%v), want IPM.Activity", v, ok)
	}
	if v, ok := msg.Props.Get(mapi.PrSubject); !ok || v != "Trip log" {
		t.Errorf("PrSubject = %v (ok=%v), want the SUMMARY", v, ok)
	}
	out := string(ExportVJournal(msg, "j-1"))
	for _, want := range []string{"BEGIN:VJOURNAL", "SUMMARY:Trip log", "DESCRIPTION:Visited the lab", "DTSTART:20260701T090000Z", "END:VJOURNAL"} {
		if !strings.Contains(out, want) {
			t.Errorf("round-trip lost %q\n%s", want, out)
		}
	}
}

// TestImportVJournalRejectsNonJournal confirms a non-journal or malformed object
// is rejected without allocating named properties or returning a partial message.
func TestImportVJournalRejectsNonJournal(t *testing.T) {
	for _, raw := range []string{
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		"TRUNCATED-JOURNAL",
	} {
		r := newResolver()
		msg, err := ImportVJournal([]byte(raw), r.opt())
		if err == nil || msg != nil {
			t.Errorf("ImportVJournal(%q) = (%v, %v), want (nil, error)", raw, msg, err)
		}
		if len(r.ids) != 0 {
			t.Errorf("refused ImportVJournal allocated %d named properties, want 0", len(r.ids))
		}
	}
}

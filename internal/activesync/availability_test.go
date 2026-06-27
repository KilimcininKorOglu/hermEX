package activesync

import (
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/ews"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// busyEvent builds one CalendarFreeBusy block for the quantization tests.
func busyEvent(start, end time.Time, busyType string) ews.CalendarEvent {
	return ews.CalendarEvent{
		StartTime: start.UTC().Format(time.RFC3339),
		EndTime:   end.UTC().Format(time.RFC3339),
		BusyType:  busyType,
	}
}

// TestQuantizeFreeBusy proves the event-to-MergedFreeBusy mapping: 30-minute
// slots, the digit codes (Free 0 / Tentative 1 / Busy 2 / OOF 3), the higher digit
// winning within a slot, any overlap claiming the whole slot, and the slot count
// rounding up.
func TestQuantizeFreeBusy(t *testing.T) {
	base := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }

	cases := []struct {
		name       string
		start, end time.Time
		events     []ews.CalendarEvent
		wantMerged string
	}{
		{
			name:  "empty calendar is all free",
			start: at(0, 0), end: at(2, 0),
			wantMerged: "0000",
		},
		{
			name:  "a busy block fills its slots",
			start: at(0, 0), end: at(2, 0),
			events:     []ews.CalendarEvent{busyEvent(at(0, 30), at(1, 30), "Busy")},
			wantMerged: "0220",
		},
		{
			name:  "OOF and busy keep their distinct digits",
			start: at(0, 0), end: at(2, 0),
			events: []ews.CalendarEvent{
				busyEvent(at(0, 0), at(0, 30), "OOF"),
				busyEvent(at(0, 30), at(1, 30), "Busy"),
			},
			wantMerged: "3220",
		},
		{
			name:  "the higher digit wins where blocks overlap a slot",
			start: at(0, 0), end: at(1, 0),
			events: []ews.CalendarEvent{
				busyEvent(at(0, 0), at(1, 0), "Busy"),
				busyEvent(at(0, 30), at(1, 0), "OOF"),
			},
			wantMerged: "23", // slot0 busy only, slot1 busy+OOF -> OOF
		},
		{
			name:  "a sub-interval block still claims the whole slot",
			start: at(0, 0), end: at(0, 30),
			// 25 free minutes then 5 busy: the slot is busy (MS-ASCMD 2.2.3.107).
			events:     []ews.CalendarEvent{busyEvent(at(0, 25), at(0, 30), "Busy")},
			wantMerged: "2",
		},
		{
			name:  "the slot count rounds up a partial slot",
			start: at(0, 0), end: at(0, 45), // 45 minutes -> 2 slots
			events:     []ews.CalendarEvent{busyEvent(at(0, 30), at(0, 45), "Busy")},
			wantMerged: "02",
		},
		{
			name:  "tentative is digit 1",
			start: at(0, 0), end: at(1, 0),
			events:     []ews.CalendarEvent{busyEvent(at(0, 0), at(0, 30), "Tentative")},
			wantMerged: "10",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := quantizeFreeBusy(c.start, c.end, c.events); got != c.wantMerged {
				t.Errorf("quantizeFreeBusy = %q, want %q", got, c.wantMerged)
			}
		})
	}
}

// TestParseAvailability proves the Options>Availability window parses, and that a
// missing element or a malformed/inverted window yields a not-ok window (so the
// response omits Availability).
func TestParseAvailability(t *testing.T) {
	start := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	avail := func(s, e string) *wbxml.Node {
		return wbxml.Elem(wbxml.RROptions, wbxml.Elem(wbxml.RRAvailability,
			wbxml.Str(wbxml.RRStartTime, s), wbxml.Str(wbxml.RREndTime, e)))
	}

	win := parseAvailability(avail(start.Format(easDateDashes), end.Format(easDateDashes)))
	if !win.ok || !win.start.Equal(start) || !win.end.Equal(end) {
		t.Errorf("parsed window = %+v, want start %v end %v ok", win, start, end)
	}
	if parseAvailability(wbxml.Elem(wbxml.RROptions)).ok {
		t.Error("no Availability element must yield a not-ok window")
	}
	if parseAvailability(avail(end.Format(easDateDashes), start.Format(easDateDashes))).ok {
		t.Error("an end before the start must yield a not-ok window")
	}
}

// availReq builds a ResolveRecipients request asking for one query's free/busy
// over the window.
func availReq(query string, start, end time.Time) *wbxml.Node {
	return wbxml.Elem(wbxml.RRResolveRecipients,
		wbxml.Str(wbxml.RRTo, query),
		wbxml.Elem(wbxml.RROptions,
			wbxml.Elem(wbxml.RRAvailability,
				wbxml.Str(wbxml.RRStartTime, start.Format(easDateDashes)),
				wbxml.Str(wbxml.RREndTime, end.Format(easDateDashes)))))
}

// TestResolveRecipientsAvailabilityOwner proves the end-to-end success path: the
// authenticated user resolving their own address with an Availability window gets
// an Availability element carrying Status 1 and a MergedFreeBusy string sized to
// the window (all-free for an empty calendar).
func TestResolveRecipientsAvailabilityOwner(t *testing.T) {
	ts, _ := seededServer(t)
	start := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour) // 1 hour -> 2 slots

	_, root := postCommand(t, ts, "ResolveRecipients", availReq(testUser, start, end))
	resp := responseFor(root, testUser)
	if resp == nil {
		t.Fatal("no Response for the query")
	}
	rec := resp.Child(wbxml.RRRecipient)
	if rec == nil {
		t.Fatal("resolved response carried no Recipient")
	}
	avail := rec.Child(wbxml.RRAvailability)
	if avail == nil {
		t.Fatal("Recipient carried no Availability")
	}
	if s := avail.ChildText(wbxml.RRStatus); s != "1" {
		t.Errorf("Availability Status = %q, want 1", s)
	}
	if mfb := avail.ChildText(wbxml.RRMergedFreeBusy); mfb != "00" {
		t.Errorf("MergedFreeBusy = %q, want \"00\" (empty calendar, 2 slots)", mfb)
	}
}

// TestResolveRecipientsAvailabilityOmitted proves that without an Availability
// request the Recipient carries no Availability element.
func TestResolveRecipientsAvailabilityOmitted(t *testing.T) {
	ts, _ := seededServer(t)
	_, root := postCommand(t, ts, "ResolveRecipients", resolveReq(testUser))
	rec := responseFor(root, testUser).Child(wbxml.RRRecipient)
	if rec.Child(wbxml.RRAvailability) != nil {
		t.Error("a request without Availability must not return an Availability element")
	}
}

// TestMergedFreeBusyGate proves the OWASP A01 free/busy permission gate: a caller
// with no free/busy right on the target's calendar is denied (no data leaks),
// while the owner sees their own calendar.
func TestMergedFreeBusyGate(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Revoke the default free/busy on the calendar so a stranger holds no
	// free/busy right (the default seed grants FrightsFreeBusySimple to everyone).
	if err := st.ModifyPermissions(int64(mapi.PrivateFIDCalendar), false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, MemberID: mapi.MemberIDDefault, Rights: mapi.FrightsVisible},
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	start := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	win := availabilityWindow{start: start, end: start.Add(time.Hour), ok: true}
	e := directory.GALEntry{Address: "target@hermex.test", StorePath: dir}

	stranger := &session{user: "stranger@hermex.test", mailbox: "/elsewhere"}
	if _, ok := mergedFreeBusy(e, win, stranger); ok {
		t.Error("a caller without free/busy permission must be denied, not shown all-free")
	}
	owner := &session{user: "target@hermex.test", mailbox: dir}
	if _, ok := mergedFreeBusy(e, win, owner); !ok {
		t.Error("the owner must see their own free/busy")
	}
}

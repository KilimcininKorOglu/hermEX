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

// availWindowStart is the StartTime the window-parsing tests send.
var availWindowStart = time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)

// availOpts wraps Availability children in an Options element.
func availOpts(children ...*wbxml.Node) *wbxml.Node {
	return wbxml.Elem(wbxml.RROptions, wbxml.Elem(wbxml.RRAvailability, children...))
}

// availTime is an EAS time element d after availWindowStart.
func availTime(tag wbxml.Tag, d time.Duration) *wbxml.Node {
	return wbxml.Str(tag, availWindowStart.Add(d).Format(easDateDashes))
}

// TestParseAvailability proves the Options>Availability windows MS-ASCMD EndTime
// (ResolveRecipients) accepts: a window parses as sent, with no EndTime the end
// defaults to seven days after StartTime, a 42-day span is still allowed, and no
// Availability element is no request at all.
func TestParseAvailability(t *testing.T) {
	start := availWindowStart
	startEl := availTime(wbxml.RRStartTime, 0)
	avail, endAt := availOpts, func(d time.Duration) *wbxml.Node { return availTime(wbxml.RREndTime, d) }

	win := parseAvailability(avail(startEl, endAt(time.Hour)))
	if !win.ok || win.invalid || !win.start.Equal(start) || !win.end.Equal(start.Add(time.Hour)) {
		t.Errorf("parsed window = %+v, want start %v end %v ok", win, start, start.Add(time.Hour))
	}
	win = parseAvailability(avail(startEl))
	if !win.ok || !win.end.Equal(start.Add(7*24*time.Hour)) {
		t.Errorf("window with no EndTime = %+v, want the end seven days after the start", win)
	}
	if win := parseAvailability(avail(startEl, endAt(42*24*time.Hour))); !win.ok {
		t.Errorf("a 42-day window = %+v, want accepted", win)
	}
	if win := parseAvailability(wbxml.Elem(wbxml.RROptions)); win.ok || win.invalid {
		t.Errorf("no Availability element = %+v, want neither requested nor invalid", win)
	}
}

// TestParseAvailabilityRefusesAnOutOfRangeWindow proves the windows the client
// may not ask for are invalid, which the command answers with Status 5: a
// missing or unreadable StartTime, an unreadable EndTime, an EndTime earlier
// than StartTime plus 30 minutes, or a span over 42 days (Exchange 2010 to 2019,
// MS-ASCMD Appendix A).
func TestParseAvailabilityRefusesAnOutOfRangeWindow(t *testing.T) {
	startEl := availTime(wbxml.RRStartTime, 0)
	avail, endAt := availOpts, func(d time.Duration) *wbxml.Node { return availTime(wbxml.RREndTime, d) }
	for name, opts := range map[string]*wbxml.Node{
		"no StartTime":            avail(endAt(time.Hour)),
		"unreadable StartTime":    avail(wbxml.Str(wbxml.RRStartTime, "tomorrow"), endAt(time.Hour)),
		"unreadable EndTime":      avail(startEl, wbxml.Str(wbxml.RREndTime, "later")),
		"end before the start":    avail(startEl, endAt(-time.Hour)),
		"end under 30 minutes":    avail(startEl, endAt(29*time.Minute)),
		"span over 42 days":       avail(startEl, endAt(42*24*time.Hour+time.Minute)),
		"span of eight millennia": avail(wbxml.Str(wbxml.RRStartTime, "1970-01-01T00:00:01.000Z"), wbxml.Str(wbxml.RREndTime, "9999-12-31T23:59:59.000Z")),
	} {
		if win := parseAvailability(opts); win.ok || !win.invalid {
			t.Errorf("%s: window = %+v, want invalid", name, win)
		}
	}
}

// TestResolveRecipientsRefusesAnInvalidWindow proves an Availability window the
// client may not ask for fails the command with ResolveRecipients Status 5
// ("Protocol error. Either an invalid parameter was specified or the range
// exceeded limits") and computes no free/busy at all, so an oversized window
// cannot make the server build a MergedFreeBusy string of millions of digits.
func TestResolveRecipientsRefusesAnInvalidWindow(t *testing.T) {
	ts, _ := seededServer(t)
	start := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	_, root := postCommand(t, ts, "ResolveRecipients", availReq(testUser, start, start.Add(43*24*time.Hour)))
	if s := root.ChildText(wbxml.RRStatus); s != "5" {
		t.Errorf("ResolveRecipients Status = %q, want 5", s)
	}
	if responseFor(root, testUser) != nil {
		t.Error("a refused request still resolved the recipient")
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
	resp := responseFor(root, testUser)
	if resp == nil || resp.Child(wbxml.RRRecipient) == nil {
		t.Fatal("the query resolved no Recipient")
	}
	if resp.Child(wbxml.RRRecipient).Child(wbxml.RRAvailability) != nil {
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
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	win := availabilityWindow{start: start, end: start.Add(time.Hour), ok: true}
	e := directory.GALEntry{Address: "target@hermex.test", StorePath: dir}

	// Neither a stranger nor one who spells the owner's address with a suffix
	// holds the owner's identity.
	for _, user := range []string{"stranger@hermex.test", "target@hermex.test.rival.test"} {
		stranger := &session{user: user, mailbox: "/elsewhere"}
		if mfb, ok := mergedFreeBusy(e, win, stranger); ok || mfb != "" {
			t.Errorf("%s: mergedFreeBusy = (%q, %v), want withheld, not shown all-free", user, mfb, ok)
		}
	}
	owner := &session{user: "target@hermex.test", mailbox: dir}
	if mfb, ok := mergedFreeBusy(e, win, owner); !ok || mfb != "00" {
		t.Errorf("owner: mergedFreeBusy = (%q, %v), want (\"00\", true): an empty calendar over two slots", mfb, ok)
	}
}

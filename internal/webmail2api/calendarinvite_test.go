package webmail2api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/meeting"
	"hermex/internal/objectstore"
)

// meetingHarness serves alice with bob as a local attendee, so a scheduling
// message alice sends is delivered into bob's mailbox.
func meetingHarness(t *testing.T) (requestFunc, string, string) {
	t.Helper()
	alice, bob := t.TempDir(), t.TempDir()
	for _, d := range []string{alice, bob} {
		st, err := objectstore.Open(d)
		mustNoErr(t, "open mailbox", err)
		st.Close()
	}
	accs := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: alice},
		"bob@hermex.test":   {MailboxPath: bob},
	}
	secret := []byte("meeting-test-secret")
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)
	return func(method, target, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: alice, Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}, alice, bob
}

// folderMail returns the wire form of every message in one of the mailbox's folders.
func folderMail(t *testing.T, dir string, fid int64) []string {
	t.Helper()
	st := openMailbox(t, dir)
	msgs, err := st.ListMessages(fid)
	mustNoErr(t, "list messages", err)
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		raw, err := st.GetMessageRaw(fid, m.UID)
		mustNoErr(t, "read message", err)
		out = append(out, string(raw))
	}
	return out
}

// storedMeetingUID is the iCalendar UID alice's copy of the meeting carries.
func storedMeetingUID(t *testing.T, dir string, id int64) string {
	t.Helper()
	st := openMailbox(t, dir)
	msg, err := st.OpenMessage(id)
	mustNoErr(t, "open meeting", err)
	return propStr(msg.Props, namedTag(t, st, mapi.NameICalUID, mapi.PtUnicode))
}

// lastOf fails unless the folder holds exactly n messages, returning the last.
func lastOf(t *testing.T, msgs []string, n int) string {
	t.Helper()
	if len(msgs) != n {
		t.Fatalf("folder holds %d messages, want %d", len(msgs), n)
	}
	return msgs[n-1]
}

// TestInvitationCarriesTheStoredMeeting sends a zoned daily series. The invitation
// used to name the meeting by alice's message id and describe only its first
// instance, so bob's copy matched nothing alice later sent and was never a series.
func TestInvitationCarriesTheStoredMeeting(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := createEvent(t, do, `{"summary":"Standup","start":"2026-09-07T06:00:00Z","end":"2026-09-07T06:30:00Z",`+
		`"timezone":"Europe/Istanbul","recurrence":"FREQ=DAILY;COUNT=3","attendees":["bob@hermex.test"],"sendInvite":true}`)

	invite := lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 1)
	for _, want := range []string{"METHOD:REQUEST", "RRULE:FREQ=DAILY;COUNT=3", "TZID=Europe/Istanbul",
		"UID:" + storedMeetingUID(t, alice, id), "ORGANIZER:mailto:alice@hermex.test"} {
		wantContains(t, "invitation", invite, want)
	}

	st := openMailbox(t, bob)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list bob's inbox", err)
	appt, err := meeting.Respond(st, directory.StaticAccounts{}, nil, "bob@hermex.test", msgs[0].ID, meeting.ResponseAccepted, false)
	mustNoErr(t, "accept", err)
	stored, err := st.OpenMessage(appt)
	mustNoErr(t, "open bob's meeting", err)
	ical, _ := stored.Props.Get(mapi.PrIcalOriginal)
	b, _ := ical.([]byte)
	wantContains(t, "bob's meeting", string(b), "RRULE:FREQ=DAILY;COUNT=3")
}

// TestInvitationUpdateAdvancesTheRevision resends an edited meeting: the attendee
// receives a request with a higher SEQUENCE, which is what makes their client take
// it as newer than the one it holds, and alice's copy records that revision.
func TestInvitationUpdateAdvancesTheRevision(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := createEvent(t, do, `{"summary":"Review","start":"2026-09-08T09:00:00Z","end":"2026-09-08T10:00:00Z",`+
		`"attendees":["bob@hermex.test"],"sendInvite":true}`)
	path := "/api/v1/calendar/events/" + strconv.FormatInt(id, 10)
	wantStatus(t, "update", do(http.MethodPut, path, `{"summary":"Review moved","start":"2026-09-08T11:00:00Z",`+
		`"end":"2026-09-08T12:00:00Z","attendees":["bob@hermex.test"],"sendInvite":true}`), http.StatusOK)

	msgs := folderMail(t, bob, int64(mapi.PrivateFIDInbox))
	wantContains(t, "resent request", lastOf(t, msgs, 2), "SEQUENCE:1")
	wantContains(t, "original request", msgs[0], "SEQUENCE:0")
	st := openMailbox(t, alice)
	msg, err := st.OpenMessage(id)
	mustNoErr(t, "open alice's meeting", err)
	seq, _ := propInt32(msg.Props, namedTag(t, st, mapi.NameAppointmentSequence, mapi.PtLong))
	wantEq(t, "alice's revision", seq, int32(1))
}

// TestInvitationDropsInjectedLines is the attendee and summary injection defect. An
// attendee that does not parse, or a summary holding a line break, reaches the To
// header, the Subject and the iCalendar lines of a mail the server relays
// externally; a line break there spliced in headers of the sender's choosing.
func TestInvitationDropsInjectedLines(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	createEvent(t, do, `{"summary":"`+injectedSummary+`","start":"2026-09-01T10:00:00Z",`+
		`"end":"2026-09-01T11:00:00Z","attendees":["bob@hermex.test","a@b.example\r\nBcc: attacker@evil.example"],"sendInvite":true}`)

	wantNoInjectedLines(t, lastOf(t, folderMail(t, alice, int64(mapi.PrivateFIDSentItems)), 1))
	lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 1)
}

// injectedSummary is an event summary that tries to add header lines.
const injectedSummary = `Sync\r\nReply-To: attacker@evil.example`

// wantNoInjectedLines fails when a line an attendee or summary tried to splice in
// reached the header block or the iCalendar of a scheduling mail.
func wantNoInjectedLines(t *testing.T, sent string) {
	t.Helper()
	header, _, _ := strings.Cut(sent, "\r\n\r\n")
	for line := range strings.SplitSeq(header, "\r\n") {
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "bcc:") || strings.HasPrefix(low, "reply-to:") {
			t.Errorf("an injected header line survived: %q", line)
		}
	}
	if strings.Contains(sent, "a@b.example") || bytes.Contains([]byte(sent), []byte("SUMMARY:Sync\r\nReply-To")) {
		t.Errorf("an injected line reached the scheduling mail:\n%s", sent)
	}
}

// seedEvent stores an event in alice's calendar directly, as another client or an
// earlier release left it.
func seedEvent(t *testing.T, dir string, e eventJSON, organizer string) int64 {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open alice", err)
	defer st.Close()
	id, err := storeEvent(st, e, int64(mapi.PrivateFIDCalendar), organizer)
	mustNoErr(t, "seed event", err)
	return id
}

// TestOrganizerDeleteCancelsUnderTheStoredUID deletes a meeting alice organizes.
// The cancellation used to name the meeting by alice's message id, which matched
// nothing bob held, and carried an empty DTSTART and no revision.
func TestOrganizerDeleteCancelsUnderTheStoredUID(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := createEvent(t, do, `{"summary":"Review","start":"2026-09-08T09:00:00Z","end":"2026-09-08T10:00:00Z",`+
		`"attendees":["bob@hermex.test"],"sendInvite":true}`)
	uid := storedMeetingUID(t, alice, id)
	deleteEvent(t, do, strconv.FormatInt(id, 10))

	cancel := lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 2)
	for _, want := range []string{"METHOD:CANCEL", "UID:" + uid, "STATUS:CANCELLED", "SEQUENCE:1", "DTSTART:20260908T090000Z"} {
		wantContains(t, "cancellation", cancel, want)
	}
	wantContains(t, "alice's Sent copy", lastOf(t, folderMail(t, alice, int64(mapi.PrivateFIDSentItems)), 2), "METHOD:CANCEL")
}

// TestAttendeeDeleteSendsNothing deletes a meeting someone else organizes. An
// attendee removing their own copy used to mail a cancellation to every other
// attendee in the organizer's name.
func TestAttendeeDeleteSendsNothing(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := seedEvent(t, alice, eventJSON{Summary: "Theirs", Start: "2026-09-03T09:00:00Z", End: "2026-09-03T10:00:00Z",
		Attendees: []string{"alice@hermex.test", "bob@hermex.test"}}, "carol@hermex.test")
	deleteEvent(t, do, strconv.FormatInt(id, 10))

	wantEq(t, "bob's inbox", len(folderMail(t, bob, int64(mapi.PrivateFIDInbox))), 0)
	wantEq(t, "alice's Sent Items", len(folderMail(t, alice, int64(mapi.PrivateFIDSentItems))), 0)
}

// TestLegacyMeetingCancelsUnderItsWireUID deletes a meeting organized before
// invitations carried the stored UID: its attendees hold it under the message id.
func TestLegacyMeetingCancelsUnderItsWireUID(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := seedEvent(t, alice, eventJSON{Summary: "Old", Start: "2026-09-02T09:00:00Z", End: "2026-09-02T10:00:00Z",
		Attendees: []string{"bob@hermex.test"}}, "")
	deleteEvent(t, do, strconv.FormatInt(id, 10))
	wantContains(t, "legacy cancellation", lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 1),
		"UID:"+strconv.FormatInt(id, 10))
}

// TestCancellationDropsInjectedLines is the summary injection defect on the
// cancellation path, which relays to external attendees as well.
func TestCancellationDropsInjectedLines(t *testing.T) {
	do, alice, _ := meetingHarness(t)
	id := createEvent(t, do, `{"summary":"`+injectedSummary+`","start":"2026-09-01T10:00:00Z",`+
		`"end":"2026-09-01T11:00:00Z","attendees":["bob@hermex.test"]}`)
	deleteEvent(t, do, strconv.FormatInt(id, 10))
	wantNoInjectedLines(t, lastOf(t, folderMail(t, alice, int64(mapi.PrivateFIDSentItems)), 1))
}

// TestLegacyMeetingKeepsItsWireUID resends a meeting organized before invitations
// carried the stored UID. Its attendees hold it under the message id the first
// invitation named, so every later message must name it the same way.
func TestLegacyMeetingKeepsItsWireUID(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := seedEvent(t, alice, eventJSON{Summary: "Old", Start: "2026-09-02T09:00:00Z", End: "2026-09-02T10:00:00Z",
		Attendees: []string{"bob@hermex.test"}}, "")

	wantStatus(t, "update", do(http.MethodPut, "/api/v1/calendar/events/"+strconv.FormatInt(id, 10),
		`{"summary":"Old","start":"2026-09-02T09:00:00Z","end":"2026-09-02T10:00:00Z","attendees":["bob@hermex.test"],"sendInvite":true}`), http.StatusOK)
	wantContains(t, "legacy request", lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 1), "UID:"+strconv.FormatInt(id, 10))
}

// TestSeriesUpdateAdvancesTheRevision resends an edited series twice. A series
// keeps its revision only in its stored iCalendar, which the revision was never
// read from, so every resend went out at SEQUENCE:1 and an attendee's client
// took the second as no newer than the first.
func TestSeriesUpdateAdvancesTheRevision(t *testing.T) {
	do, _, bob := meetingHarness(t)
	id := createEvent(t, do, `{"summary":"Standup","start":"2026-09-07T06:00:00Z","end":"2026-09-07T06:30:00Z",`+
		`"recurrence":"FREQ=DAILY;COUNT=3","attendees":["bob@hermex.test"],"sendInvite":true}`)
	for _, summary := range []string{"Standup moved", "Standup moved again"} {
		wantStatus(t, "update", do(http.MethodPut, "/api/v1/calendar/events/"+strconv.FormatInt(id, 10),
			`{"summary":"`+summary+`","start":"2026-09-07T06:00:00Z","end":"2026-09-07T06:30:00Z",`+
				`"recurrence":"FREQ=DAILY;COUNT=3","attendees":["bob@hermex.test"],"sendInvite":true}`), http.StatusOK)
	}
	wantContains(t, "second resend", lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 3), "SEQUENCE:2")
}

// seriesBody is a daily three-instance meeting alice organizes with bob.
const seriesBody = `{"summary":"Standup","start":"2026-09-07T06:00:00Z","end":"2026-09-07T06:30:00Z",` +
	`"recurrence":"FREQ=DAILY;COUNT=3","attendees":["bob@hermex.test"],"sendInvite":true}`

// TestOccurrenceDeleteCancelsThatInstance deletes the second instance of a series
// alice organizes. It used to change alice's calendar and tell nobody, so bob kept
// attending an instance that no longer took place.
func TestOccurrenceDeleteCancelsThatInstance(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := createEvent(t, do, seriesBody)
	wantStatus(t, "delete occurrence", do(http.MethodDelete, occurrencePath(id)+"?at=2026-09-08T06:00:00Z", ""), http.StatusOK)

	cancel := lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 2)
	for _, want := range []string{"METHOD:CANCEL", "UID:" + storedMeetingUID(t, alice, id), "RECURRENCE-ID:20260908T060000Z",
		"STATUS:CANCELLED", "SEQUENCE:1"} {
		wantContains(t, "occurrence cancellation", cancel, want)
	}
	if strings.Contains(cancel, "RRULE") {
		t.Errorf("the occurrence cancellation carries the series rule, so it reads as cancelling the whole series:\n%s", cancel)
	}
	st := openMailbox(t, alice)
	msg, err := st.OpenMessage(id)
	mustNoErr(t, "open alice's series", err)
	wantEq(t, "alice's revision", storedSequence(st, msg.Props), 1)
}

// TestAttendeeOccurrenceDeleteSendsNothing deletes an instance of a series someone
// else organizes: only the attendee's own copy changes.
func TestAttendeeOccurrenceDeleteSendsNothing(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := seedEvent(t, alice, eventJSON{Summary: "Theirs", Start: "2026-09-07T06:00:00Z", End: "2026-09-07T06:30:00Z",
		Recurrence: "FREQ=DAILY;COUNT=3", Attendees: []string{"alice@hermex.test", "bob@hermex.test"}}, "carol@hermex.test")
	wantStatus(t, "delete occurrence", do(http.MethodDelete, occurrencePath(id)+"?at=2026-09-08T06:00:00Z", ""), http.StatusOK)
	wantEq(t, "bob's inbox", len(folderMail(t, bob, int64(mapi.PrivateFIDInbox))), 0)
}

// TestOccurrenceMoveUpdatesThatInstance moves the second instance of a series
// alice organizes. It used to change alice's calendar and tell nobody; now bob
// receives a request for that instance alone, and accepting it folds the move
// into his copy of the series.
func TestOccurrenceMoveUpdatesThatInstance(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := createEvent(t, do, seriesBody)
	st := openMailbox(t, bob)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list bob's inbox", err)
	appt, err := meeting.Respond(st, directory.StaticAccounts{}, nil, "bob@hermex.test", msgs[0].ID, meeting.ResponseAccepted, false)
	mustNoErr(t, "accept the series", err)

	wantStatus(t, "move occurrence", do(http.MethodPut, occurrencePath(id), `{"occurrence":"2026-09-08T06:00:00Z",`+
		`"start":"2026-09-08T09:00:00Z","end":"2026-09-08T09:30:00Z"}`), http.StatusOK)
	update := lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 2)
	for _, want := range []string{"METHOD:REQUEST", "UID:" + storedMeetingUID(t, alice, id), "RECURRENCE-ID:20260908T060000Z",
		"DTSTART:20260908T090000Z", "SEQUENCE:1"} {
		wantContains(t, "occurrence request", update, want)
	}

	msgs, err = st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list bob's inbox", err)
	_, err = meeting.Respond(st, directory.StaticAccounts{}, nil, "bob@hermex.test", msgs[1].ID, meeting.ResponseAccepted, false)
	mustNoErr(t, "accept the move", err)
	stored, err := st.OpenMessage(appt)
	mustNoErr(t, "open bob's series", err)
	ical, _ := stored.Props.Get(mapi.PrIcalOriginal)
	b, _ := ical.([]byte)
	wantContains(t, "bob's series", string(b), "RRULE:FREQ=DAILY;COUNT=3")
	wantContains(t, "bob's series", string(b), "DTSTART:20260908T090000Z")
}

// TestMeetingSettingsRoundTrip reads the cancellation setting on as a fresh
// mailbox's default, turns it off, and proves a PUT that names only one field
// leaves the other and the operator's automatic acceptance as they were.
func TestMeetingSettingsRoundTrip(t *testing.T) {
	do, alice, _ := meetingHarness(t)
	st := openMailbox(t, alice)
	mustNoErr(t, "seed operator setting", st.SetMeetingConfig(objectstore.MeetingConfig{AutoAccept: true}))

	got := okBody[meetingSettingsJSON](t, "get", do(http.MethodGet, "/api/v1/settings/meeting", ""))
	wantEq(t, "default processing", *got.ProcessCancellations, true)
	okBody[meetingSettingsJSON](t, "put", do(http.MethodPut, "/api/v1/settings/meeting", `{"removeRequestOnResponse":true}`))
	got = okBody[meetingSettingsJSON](t, "put", do(http.MethodPut, "/api/v1/settings/meeting", `{"processCancellations":false}`))
	wantEq(t, "processing after PUT", *got.ProcessCancellations, false)
	wantEq(t, "remove request kept", *got.RemoveRequestOnResponse, true)

	cfg, err := st.GetMeetingConfig()
	mustNoErr(t, "read config", err)
	wantEq(t, "stored config", cfg, objectstore.MeetingConfig{AutoAccept: true, RemoveRequestOnResponse: true, LeaveCancellationsUnprocessed: true})
}

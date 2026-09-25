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

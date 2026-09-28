package ews

import (
	"bytes"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/oxews"
	"hermex/internal/relay"
)

// meetingResponseReqWith builds a meeting response whose response object carries
// the given extra child elements after its ReferenceItemId.
func meetingResponseReqWith(verb, refItemID, disp, extra string) string {
	return wrapRequest(`<CreateItem MessageDisposition="` + disp + `" xmlns="` + nsMessages + `">` +
		`<Items><t:` + verb + ` xmlns:t="` + nsTypes + `">` +
		`<t:ReferenceItemId Id="` + refItemID + `"/>` + extra +
		`</t:` + verb + `></Items></CreateItem>`)
}

// respondAndClaim answers an external organizer's meeting request with the given
// response object and returns the one reply the relay spool queued for them.
func respondAndClaim(t *testing.T, verb, extra string) []byte {
	t.Helper()
	dir := t.TempDir()
	reqID := seedExternalMeetingRequest(t, dir)
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sp, err := relay.Open(filepath.Join(t.TempDir(), "relay.sqlite3"))
	mustNoErr(t, "open the relay spool", err)
	t.Cleanup(func() { _ = sp.Close() })
	srv.Spool = sp
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDInbox), MessageID: reqID})
	_, out := soapPost(t, ts, meetingResponseReqWith(verb, itemID, "SendAndSaveCopy", extra), true)
	wantContains(t, "the "+verb+" response class", out, `ResponseClass="Success"`)
	due, err := sp.Claim(time.Now(), 10)
	mustNoErr(t, "claim the spooled reply", err)
	if len(due) != 1 {
		t.Fatalf("relay spool = %d messages, want the one reply", len(due))
	}
	return due[0].Body
}

// TestMeetingResponseProposesANewTime proves a response object's ProposedStart and
// ProposedEnd ([MS-OXWSMTGS] 2.2.4.16) reach the organizer as a counter proposal
// for that span ([MS-OXCICAL] METHOD: COUNTER).
func TestMeetingResponseProposesANewTime(t *testing.T) {
	raw := respondAndClaim(t, "TentativelyAcceptItem",
		`<t:ProposedStart>2026-07-01T16:00:00Z</t:ProposedStart><t:ProposedEnd>2026-07-01T17:00:00Z</t:ProposedEnd>`)
	for _, want := range []string{"METHOD:COUNTER", "DTSTART:20260701T160000Z", "DTEND:20260701T170000Z", "PARTSTAT=TENTATIVE"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("the proposal lacks %q:\n%s", want, raw)
		}
	}
}

// TestMeetingResponseRefusesABadProposal proves a proposed span the organizer
// cannot be offered is refused, and nothing is sent: one end alone is an invalid
// request, and a span that ends before it starts gets Exchange's code for it.
func TestMeetingResponseRefusesABadProposal(t *testing.T) {
	for _, tc := range []struct{ name, extra, want string }{
		{"one end", `<t:ProposedStart>2026-07-01T16:00:00Z</t:ProposedStart>`, "ErrorInvalidRequest"},
		{"ends first", `<t:ProposedStart>2026-07-01T17:00:00Z</t:ProposedStart><t:ProposedEnd>2026-07-01T16:00:00Z</t:ProposedEnd>`,
			"ErrorCalendarEndDateIsEarlierThanStartDate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			reqID := seedExternalMeetingRequest(t, dir)
			ts := meetingServer(t, dir)
			itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: int64(mapi.PrivateFIDInbox), MessageID: reqID})
			_, out := soapPost(t, ts, meetingResponseReqWith("TentativelyAcceptItem", itemID, "SendAndSaveCopy", tc.extra), true)
			wantContains(t, "the refusal", out, "<ResponseCode>"+tc.want+"</ResponseCode>")
		})
	}
}

// TestMeetingResponseCarriesTheAttendeesNote proves the note an attendee writes in
// an AcceptItem's Body reaches the organizer with the response ([MS-OXWSMTGS]
// AcceptItem: Body), in the body type the client wrote it in, and that the
// invitation's own text is not what the organizer gets instead.
func TestMeetingResponseCarriesTheAttendeesNote(t *testing.T) {
	for _, tc := range []struct {
		name, body, want, wantType string
	}{
		{"text", `<t:Body BodyType="Text">I will join from the Berlin office.</t:Body>`,
			"I will join from the Berlin office.", "text/plain"},
		{"html", `<t:Body BodyType="HTML">&lt;p&gt;Joining &lt;b&gt;remotely&lt;/b&gt;&lt;/p&gt;</t:Body>`,
			"<p>Joining <b>remotely</b></p>", "text/html"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := respondAndClaim(t, "AcceptItem", tc.body)
			if !bytes.Contains(raw, []byte(tc.want)) {
				t.Errorf("the reply lacks the attendee's note %q:\n%s", tc.want, raw)
			}
			if !bytes.Contains(raw, []byte("Content-Type: "+tc.wantType)) {
				t.Errorf("the note is not sent as %s:\n%s", tc.wantType, raw)
			}
			if !bytes.Contains(raw, []byte("METHOD:REPLY")) {
				t.Errorf("the reply lost its iTIP part:\n%s", raw)
			}
		})
	}
}

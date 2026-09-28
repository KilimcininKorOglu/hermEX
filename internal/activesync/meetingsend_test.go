package activesync

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

const localOrganizer = "organizer@hermex.test"

// organizerServer starts a server whose directory also holds the meeting's
// organizer, so a response the server sends is delivered to a mailbox the test
// can read. It returns the attendee's and the organizer's mailbox paths.
func organizerServer(t *testing.T) (ts *httptest.Server, attendee, organizer string) {
	t.Helper()
	attendee = filepath.Join(t.TempDir(), "alice")
	organizer = filepath.Join(t.TempDir(), "organizer")
	for _, dir := range []string{attendee, organizer} {
		st, err := objectstore.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	accs := directory.StaticAccounts{
		testUser:       {Password: testPass, MailboxPath: attendee},
		localOrganizer: {Password: testPass, MailboxPath: organizer},
	}
	ts = httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, attendee, organizer
}

// seedLocalRequest delivers a meeting request from the local organizer into the
// attendee's Inbox and returns its IMAP UID.
func seedLocalRequest(t *testing.T, dir, ics string) uint32 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := "From: " + localOrganizer + "\r\nTo: " + testUser + "\r\nSubject: Planning\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/calendar; method=REQUEST; charset=UTF-8\r\n\r\n" + ics
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID
}

const planningICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
	"BEGIN:VEVENT\r\nUID:eas-send-1\r\nDTSTAMP:20260601T080000Z\r\nSUMMARY:Planning\r\n" +
	"DTSTART:20260702T140000Z\r\nDTEND:20260702T150000Z\r\n" +
	"ORGANIZER:mailto:" + localOrganizer + "\r\nATTENDEE;RSVP=TRUE:mailto:" + testUser + "\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// seedRequestWithText delivers a request whose mail carries the organizer's own
// text beside the calendar part, and returns its IMAP UID.
func seedRequestWithText(t *testing.T, dir string) uint32 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := "From: " + localOrganizer + "\r\nTo: " + testUser + "\r\nSubject: Planning\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nOrganizer agenda text.\r\n" +
		"--b\r\nContent-Type: text/calendar; method=REQUEST; charset=UTF-8\r\n\r\n" + planningICS +
		"--b--\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.UID
}

// postVersioned POSTs a command under the given MS-ASProtocolVersion.
func postVersioned(t *testing.T, ts *httptest.Server, version, cmd string, root *wbxml.Node) *wbxml.Node {
	t.Helper()
	url := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=" + cmd + "&User=" + testUser + "&DeviceId=dev1&DeviceType=iPhone"
	req, err := http.NewRequest("POST", url, bytes.NewReader(wbxml.Marshal(root)))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, testPass)
	req.Header.Set("Content-Type", "application/vnd.ms-sync.wbxml")
	req.Header.Set("MS-ASProtocolVersion", version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s status %d: %s", cmd, resp.StatusCode, out)
	}
	node, err := wbxml.Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return node
}

// acceptRequest answers the Inbox request uid with Accept, adding extra children.
func acceptRequest(t *testing.T, ts *httptest.Server, version string, uid uint32, extra ...*wbxml.Node) *wbxml.Node {
	t.Helper()
	children := append([]*wbxml.Node{
		wbxml.Str(wbxml.MRUserResponse, "1"),
		wbxml.Str(wbxml.MRFolderID, strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10)),
		wbxml.Str(wbxml.MRRequestID, strconv.FormatUint(uint64(uid), 10)),
	}, extra...)
	root := postVersioned(t, ts, version, "MeetingResponse",
		wbxml.Elem(wbxml.MRMeetingResponse, wbxml.Elem(wbxml.MRRequest, children...)))
	result := root.Child(wbxml.MRResult)
	if result == nil || result.ChildText(wbxml.MRStatus) != "1" {
		t.Fatalf("MeetingResponse did not succeed: %+v", root)
	}
	return result
}

// organizerInbox returns the raw messages in the organizer's Inbox.
func organizerInbox(t *testing.T, dir string) [][]byte {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, m := range msgs {
		raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDInbox), m.UID)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, raw)
	}
	return out
}

// TestMeetingResponseSendRule proves who sends the organizer the answer ([MS-ASCMD]
// MeetingResponse): through 14.1 the client does it with SendMail, so the server
// sends nothing; from 16.0 the server sends it only when SendResponse is present.
func TestMeetingResponseSendRule(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		send    bool
		want    int
	}{
		{"14.1 never", "14.1", true, 0},
		{"16.1 without SendResponse", "16.1", false, 0},
		{"16.1 with SendResponse", "16.1", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, attendee, organizer := organizerServer(t)
			uid := seedLocalRequest(t, attendee, planningICS)
			var extra []*wbxml.Node
			if tc.send {
				extra = append(extra, wbxml.Elem(wbxml.MRSendResponse))
			}
			acceptRequest(t, ts, tc.version, uid, extra...)
			if got := len(organizerInbox(t, organizer)); got != tc.want {
				t.Errorf("organizer received %d responses, want %d", got, tc.want)
			}
		})
	}
}

// TestMeetingResponseBody proves the response carries the text the attendee wrote
// in SendResponse, and never the invitation's own text ([MS-ASCMD] SendResponse:
// an empty node sends a response with no body).
func TestMeetingResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		body *wbxml.Node
		want string
	}{
		{"empty", wbxml.Elem(wbxml.MRSendResponse), ""},
		{"with body", wbxml.Elem(wbxml.MRSendResponse, wbxml.Elem(wbxml.ABBody,
			wbxml.Str(wbxml.ABType, "1"), wbxml.Str(wbxml.ABData, "See you there."))), "See you there."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, attendee, organizer := organizerServer(t)
			uid := seedRequestWithText(t, attendee)
			acceptRequest(t, ts, "16.1", uid, tc.body)
			got := organizerInbox(t, organizer)
			if len(got) != 1 {
				t.Fatalf("organizer received %d responses, want 1", len(got))
			}
			if bytes.Contains(got[0], []byte("Organizer agenda text.")) {
				t.Errorf("the response repeats the invitation's text:\n%s", got[0])
			}
			if tc.want != "" && !bytes.Contains(got[0], []byte(tc.want)) {
				t.Errorf("the response lacks the attendee's text %q:\n%s", tc.want, got[0])
			}
		})
	}
}

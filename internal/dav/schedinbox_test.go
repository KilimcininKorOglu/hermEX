package dav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// bobDav issues a DAV request authenticated as the peer user bob (davServerWithPeer
// authorizes both the test user and bob).
func bobDav(t *testing.T, ts *httptest.Server, method, path, depth, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	req.SetBasicAuth("bob@hermex.test", testPass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(out)
}

// TestScheduleInboxReceiving covers the pure-CalDAV receiving path (RFC 6638 §4.1):
// an organizer's PUT delivers an invite to the attendee, whose scheduling Inbox
// collection then lists it, GET returns a valid iTIP REQUEST, and DELETE (the
// accept-then-delete flow) removes it.
func TestScheduleInboxReceiving(t *testing.T) {
	const bob = "bob@hermex.test"
	ts, _ := davServerWithPeer(t)

	body := schedEvent("inv-1", testUser, "Review", "20260701T140000Z", 0, bob)
	if resp, out := doFull(t, ts, "PUT", "/dav/calendars/"+testUser+"/calendar/inv-1.ics", body, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("organizer PUT status %d, want 201\n%s", resp.StatusCode, out)
	}

	inbox := "/dav/calendars/" + bob + "/inbox/"
	resp, listing := bobDav(t, ts, "PROPFIND", inbox, "1", "")
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("inbox PROPFIND status %d, want 207\n%s", resp.StatusCode, listing)
	}
	m := regexp.MustCompile(`/inbox/(\d+\.ics)`).FindStringSubmatch(listing)
	if m == nil {
		t.Fatalf("scheduling Inbox lists no member after an invite was delivered\n%s", listing)
	}
	member := inbox + m[1]

	resp, ics := bobDav(t, ts, "GET", member, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inbox GET status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(ics, "METHOD:REQUEST") || !strings.Contains(ics, "BEGIN:VEVENT") {
		t.Errorf("inbox object is not a valid iTIP REQUEST:\n%s", ics)
	}

	if resp, _ := bobDav(t, ts, "DELETE", member, "", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("inbox DELETE status %d, want 204", resp.StatusCode)
	}
	if _, after := bobDav(t, ts, "PROPFIND", inbox, "1", ""); regexp.MustCompile(`/inbox/\d+\.ics`).MatchString(after) {
		t.Errorf("scheduling Inbox still lists a member after DELETE:\n%s", after)
	}
}

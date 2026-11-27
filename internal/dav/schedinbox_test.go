package dav

import (
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
	return resp, readBody(t, resp)
}

// TestScheduleInboxReceiving covers the pure-CalDAV receiving path (RFC 6638 §4.1):
// an organizer's PUT delivers an invite to the attendee, whose scheduling Inbox
// collection then lists it, GET returns a valid iTIP REQUEST, and DELETE (the
// accept-then-delete flow) removes it.
func TestScheduleInboxReceiving(t *testing.T) {
	const bob = "bob@hermex.test"
	ts, bobDir := davServerWithPeer(t)

	body := schedEvent("inv-1", testUser, "Review", "20260701T140000Z", 0, bob)
	if resp, out := doFull(t, ts, "PUT", "/dav/calendars/"+testUser+"/calendar/inv-1.ics", body, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("organizer PUT status %d, want 201\n%s", resp.StatusCode, out)
	}
	if n := inboxMessage(t, bobDir).count; n != 1 {
		t.Fatalf("attendee Inbox holds %d messages after the invite, want 1", n)
	}

	inbox := "/dav/calendars/" + bob + "/inbox/"
	member := inbox + inboxMember(t, ts, inbox)

	resp, ics := bobDav(t, ts, "GET", member, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inbox GET status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(ics, "METHOD:REQUEST") || !strings.Contains(ics, "BEGIN:VEVENT") {
		t.Errorf("inbox object is not a valid iTIP REQUEST:\n%s", ics)
	}

	if resp, out := bobDav(t, ts, "DELETE", member, "", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("inbox DELETE status %d, want 204\n%s", resp.StatusCode, out)
	}
	if listing := inboxListing(t, ts, inbox); inboxMemberRE.MatchString(listing) {
		t.Errorf("scheduling Inbox still lists a member after DELETE:\n%s", listing)
	}
	if n := inboxMessage(t, bobDir).count; n != 0 {
		t.Errorf("attendee Inbox holds %d messages after DELETE, want 0", n)
	}
}

// inboxMemberRE matches a scheduling Inbox member href and captures its name.
var inboxMemberRE = regexp.MustCompile(`/inbox/(\d+\.ics)`)

// inboxListing PROPFINDs bob's scheduling Inbox and requires a 207.
func inboxListing(t *testing.T, ts *httptest.Server, inbox string) string {
	t.Helper()
	resp, listing := bobDav(t, ts, "PROPFIND", inbox, "1", "")
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("inbox PROPFIND status %d, want 207\n%s", resp.StatusCode, listing)
	}
	return listing
}

// inboxMember returns the name of the one member bob's scheduling Inbox lists.
func inboxMember(t *testing.T, ts *httptest.Server, inbox string) string {
	t.Helper()
	listing := inboxListing(t, ts, inbox)
	m := inboxMemberRE.FindStringSubmatch(listing)
	if m == nil {
		t.Fatalf("scheduling Inbox lists no member after an invite was delivered\n%s", listing)
	}
	return m[1]
}

package dav

import (
	"net/http"
	"strings"
	"testing"
)

// taskBody is a VTODO whose UID differs from the resource name it is PUT under,
// as clients that name resources by a random path do.
func taskBody(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VTODO\r\nUID:" + uid + "\r\nDTSTAMP:20260620T100000Z\r\nSUMMARY:" + summary + "\r\n" +
		"END:VTODO\r\nEND:VCALENDAR\r\n"
}

// TestTaskKeepsTheUIDItWasStoredWith stores a task under a resource name unlike
// its UID. RFC 5545 3.8.4.7 makes the UID the component's persistent identity and
// RFC 4791 4.1 keys a calendar object resource by it, so a GET and a
// calendar-multiget must answer with the UID the client sent, not the resource
// name; a client that matches by UID would otherwise see a second task.
func TestTaskKeepsTheUIDItWasStoredWith(t *testing.T) {
	ts := davServerCal(t)
	taskURL := "/dav/calendars/" + testUser + "/tasks/9f2c.ics"
	const uid = "task-7d1e@client.example"

	if resp, body := doFull(t, ts, "PUT", taskURL, taskBody(uid, "Buy milk"), map[string]string{"Content-Type": "text/calendar"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d: %s", resp.StatusCode, body)
	}
	resp, body := do(t, ts, "GET", taskURL, "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "UID:"+uid+"\r\n") {
		t.Errorf("GET does not carry the stored UID %q:\n%s", uid, body)
	}

	multiget := `<?xml version="1.0"?><C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:prop><C:calendar-data/></D:prop><D:href>` + taskURL + `</D:href></C:calendar-multiget>`
	if resp, rep := doFull(t, ts, "REPORT", "/dav/calendars/"+testUser+"/tasks/", multiget, map[string]string{"Content-Type": "application/xml"}); resp.StatusCode != http.StatusMultiStatus || !strings.Contains(rep, "UID:"+uid) {
		t.Errorf("multiget status = %d, want 207 carrying UID %q:\n%s", resp.StatusCode, uid, rep)
	}
}

// TestTaskPutReplacesTheStoredUID rewrites a task under the same name with a new
// UID; the stored identity follows the latest body rather than keeping the first.
func TestTaskPutReplacesTheStoredUID(t *testing.T) {
	ts := davServerCal(t)
	taskURL := "/dav/calendars/" + testUser + "/tasks/a1.ics"
	for _, uid := range []string{"first-uid", "second-uid"} {
		if resp, body := doFull(t, ts, "PUT", taskURL, taskBody(uid, "Call"), map[string]string{"Content-Type": "text/calendar"}); resp.StatusCode >= 300 {
			t.Fatalf("PUT %s status = %d: %s", uid, resp.StatusCode, body)
		}
	}
	_, body := do(t, ts, "GET", taskURL, "", true)
	if !strings.Contains(body, "UID:second-uid\r\n") || strings.Contains(body, "first-uid") {
		t.Errorf("GET after the second PUT does not carry only the new UID:\n%s", body)
	}
}

// TestAStoredTaskUIDCannotAddAProperty stores a UID whose TEXT escape decodes to a
// line break followed by a property. The UID is client data that is now echoed
// back, so the GET must re-escape it (RFC 5545 3.3.11) and never emit the
// smuggled property as a content line of its own.
func TestAStoredTaskUIDCannotAddAProperty(t *testing.T) {
	ts := davServerCal(t)
	taskURL := "/dav/calendars/" + testUser + "/tasks/inj.ics"
	if resp, body := doFull(t, ts, "PUT", taskURL, taskBody(`evil\nX-INJECTED:1`, "Inject"), map[string]string{"Content-Type": "text/calendar"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d: %s", resp.StatusCode, body)
	}
	_, got := do(t, ts, "GET", taskURL, "", true)
	if strings.Contains(got, "\nX-INJECTED:") || strings.Contains(got, "\rX-INJECTED:") {
		t.Errorf("the stored UID added a content line:\n%s", got)
	}
	if !strings.Contains(got, `UID:evil\nX-INJECTED:1`) {
		t.Errorf("the UID is not served back escaped:\n%s", got)
	}
}

// TestTaskWithoutAUIDIsServedUnderItsName stores a VTODO that carries no UID;
// RFC 5545 requires one, so the server still answers with a UID: the resource
// name, which is stable for the object.
func TestTaskWithoutAUIDIsServedUnderItsName(t *testing.T) {
	ts := davServerCal(t)
	taskURL := "/dav/calendars/" + testUser + "/tasks/nouid.ics"
	body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VTODO\r\nDTSTAMP:20260620T100000Z\r\nSUMMARY:Water plants\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	if resp, rb := doFull(t, ts, "PUT", taskURL, body, map[string]string{"Content-Type": "text/calendar"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d: %s", resp.StatusCode, rb)
	}
	_, got := do(t, ts, "GET", taskURL, "", true)
	if !strings.Contains(got, "UID:nouid.ics\r\n") {
		t.Errorf("a task stored without a UID is not served under its name:\n%s", got)
	}
}

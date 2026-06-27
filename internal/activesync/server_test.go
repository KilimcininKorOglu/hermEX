package activesync

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

const (
	testUser = "alice@hermex.test"
	testPass = "test1234"
)

// testServer starts an ActiveSync server authorizing the test user.
func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// do issues a request to the test server, optionally with Basic auth.
func do(t *testing.T, ts *httptest.Server, method, path, body string, auth bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth {
		req.SetBasicAuth(testUser, testPass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

// TestOptions confirms OPTIONS advertises the supported protocol versions (14.x and
// 16.x), the highest in MS-Server-ActiveSync, and the command set.
func TestOptions(t *testing.T) {
	ts := testServer(t)
	resp, _ := do(t, ts, "OPTIONS", "/Microsoft-Server-ActiveSync", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	v := resp.Header.Get("MS-ASProtocolVersions")
	for _, want := range []string{"14.0", "14.1", "16.0", "16.1"} {
		if !strings.Contains(v, want) {
			t.Errorf("MS-ASProtocolVersions = %q, missing %s", v, want)
		}
	}
	if got := resp.Header.Get("MS-Server-ActiveSync"); got != "16.1" {
		t.Errorf("MS-Server-ActiveSync = %q, want 16.1 (highest)", got)
	}
	if c := resp.Header.Get("MS-ASProtocolCommands"); !strings.Contains(c, "FolderSync") || !strings.Contains(c, "Sync") {
		t.Errorf("MS-ASProtocolCommands = %q lacks the core commands", c)
	}
}

// TestProtocolNegotiation confirms a client's advertised version is honored and an
// unadvertised one falls back to the conservative floor.
func TestProtocolNegotiation(t *testing.T) {
	for _, tc := range []struct{ sent, want string }{
		{"16.1", "16.1"},
		{"16.0", "16.0"},
		{"14.1", "14.1"},
		{"", "14.1"},     // no header -> floor
		{"2.5", "14.1"},  // unadvertised -> floor
		{"99.9", "14.1"}, // unknown -> floor
	} {
		r := httptest.NewRequest("POST", "/Microsoft-Server-ActiveSync", nil)
		if tc.sent != "" {
			r.Header.Set("MS-ASProtocolVersion", tc.sent)
		}
		if got := protocolVersion(r); got != tc.want {
			t.Errorf("protocolVersion(%q) = %q, want %q", tc.sent, got, tc.want)
		}
	}
}

// TestUnauthenticated confirms the endpoint challenges a request with no creds.
func TestUnauthenticated(t *testing.T) {
	ts := testServer(t)
	resp, _ := do(t, ts, "OPTIONS", "/Microsoft-Server-ActiveSync", "", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("missing WWW-Authenticate challenge")
	}
}

// TestPlainQueryDispatch confirms a plain-query command reaches dispatch.
// GetAttachment is not implemented in v1, so it returns 501 — proving the
// transport and parse path works end to end (implemented commands are exercised
// by the command tests).
func TestPlainQueryDispatch(t *testing.T) {
	ts := testServer(t)
	resp, _ := do(t, ts, "POST", "/Microsoft-Server-ActiveSync?Cmd=GetAttachment&User="+testUser+"&DeviceId=dev1&DeviceType=iPhone", "", true)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d, want 501 (dispatch reached)", resp.StatusCode)
	}
}

// TestParseBase64Query checks the MS-ASHTTP base64-packed command string is
// decoded into the right command and device fields.
func TestParseBase64Query(t *testing.T) {
	packed := []byte{
		0x0E,       // protocol version (informational)
		0x09,       // command code 9 = FolderSync
		0x09, 0x04, // locale
		0x04, 'd', 'e', 'v', '1', // device id, length 4
		0x00,                               // policy key length 0
		0x06, 'i', 'P', 'h', 'o', 'n', 'e', // device type, length 6
	}
	req, err := parseBase64Query(base64.StdEncoding.EncodeToString(packed))
	if err != nil {
		t.Fatalf("parseBase64Query: %v", err)
	}
	if req.cmd != "FolderSync" {
		t.Errorf("cmd = %q, want FolderSync", req.cmd)
	}
	if req.deviceID != "dev1" {
		t.Errorf("deviceID = %q, want dev1", req.deviceID)
	}
	if req.deviceType != "iPhone" {
		t.Errorf("deviceType = %q, want iPhone", req.deviceType)
	}
}

// TestBase64QueryDispatch confirms a base64-packed command also routes through
// the live endpoint to dispatch (command code 4 = GetAttachment, unimplemented in v1).
func TestBase64QueryDispatch(t *testing.T) {
	ts := testServer(t)
	packed := []byte{0x0E, 0x04, 0x09, 0x04, 0x04, 'd', 'e', 'v', '1', 0x00, 0x03, 'A', 'n', 'd'}
	resp, _ := do(t, ts, "POST", "/Microsoft-Server-ActiveSync?"+base64.StdEncoding.EncodeToString(packed), "", true)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d, want 501 (base64 command reached dispatch)", resp.StatusCode)
	}
}

// TestOptionsAdvertisesDispatchedCommands proves the OPTIONS capability header
// advertises every command the server dispatches: a command the server handles
// but omits here is one clients never send. Each advertised command is confirmed
// to reach a handler (an empty body may error, but never the 501 not-implemented
// path).
func TestOptionsAdvertisesDispatchedCommands(t *testing.T) {
	ts := testServer(t)
	resp, _ := do(t, ts, "OPTIONS", "/Microsoft-Server-ActiveSync", "", true)
	advertised := resp.Header.Get("MS-ASProtocolCommands")
	if advertised == "" {
		t.Fatal("OPTIONS advertised no commands")
	}
	for _, cmd := range supportedCommands {
		if !strings.Contains(","+advertised+",", ","+cmd+",") {
			t.Errorf("MS-ASProtocolCommands omits %s: %q", cmd, advertised)
		}
		r, _ := do(t, ts, "POST", "/Microsoft-Server-ActiveSync?Cmd="+cmd+"&User="+testUser+"&DeviceId=dev1&DeviceType=iPhone", "", true)
		if r.StatusCode == http.StatusNotImplemented {
			t.Errorf("advertised command %s is not dispatched (501)", cmd)
		}
	}
}

// TestAutodiscover confirms the mobilesync Autodiscover response carries the
// ActiveSync URL and the authenticated identity.
func TestAutodiscover(t *testing.T) {
	ts := testServer(t)
	body := `<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/mobilesync/requestschema/2006"><Request><EMailAddress>` + testUser + `</EMailAddress></Request></Autodiscover>`
	resp, out := do(t, ts, "POST", "/autodiscover/autodiscover.xml", body, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200\n%s", resp.StatusCode, out)
	}
	for _, want := range []string{"MobileSync", "https://mail.hermex.test/Microsoft-Server-ActiveSync", testUser} {
		if !strings.Contains(out, want) {
			t.Errorf("autodiscover response missing %q\n%s", want, out)
		}
	}
}

// TestAutodiscoverUnauthenticated confirms Autodiscover requires credentials.
func TestAutodiscoverUnauthenticated(t *testing.T) {
	ts := testServer(t)
	resp, _ := do(t, ts, "POST", "/autodiscover/autodiscover.xml", "", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", resp.StatusCode)
	}
}

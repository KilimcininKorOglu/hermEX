package activesync

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
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

// TestRequestsNeverCreateAMissingMailbox proves an ActiveSync command for an
// account whose mailbox is missing fails and leaves it missing. A mailbox is made
// when its account is created; a lost one (a deleted directory, an unmounted
// volume) must not come back as an empty mailbox a device then syncs, wiping the
// device's own copy of the mail.
func TestRequestsNeverCreateAMissingMailbox(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: gone}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)

	code := rawCommandStatus(t, ts, "FolderSync", wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, "0")))
	if code == http.StatusOK {
		t.Errorf("FolderSync on a missing mailbox = %d, want a failure", code)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("an ActiveSync command created the missing mailbox: %v", err)
	}
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
	// The advertised command set must stay in step with the dispatch switch: a
	// dispatched command omitted here is one the client never uses. Find (the 16.1
	// search) and MeetingResponse (calendar accept/decline) are dispatched, so a
	// server claiming 16.1 must advertise them.
	cmds := resp.Header.Get("MS-ASProtocolCommands")
	for _, want := range []string{
		"Provision", "FolderSync", "Sync", "MeetingResponse", "SendMail",
		"SmartForward", "SmartReply", "Ping", "Settings", "ItemOperations",
		"MoveItems", "ResolveRecipients", "Search", "Find", "ValidateCert",
	} {
		if !strings.Contains(cmds, want) {
			t.Errorf("MS-ASProtocolCommands = %q, missing %s", cmds, want)
		}
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
// GetAttachment is not implemented in v1, so it returns 501, proving the
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

// TestQueryRefusesAMalformedDeviceID proves the device id follows MS-ASHTTP. In
// the plain query the device-id ABNF rule is 1*32(ALPHA / DIGIT); in the
// base64-packed form the Device ID length byte MUST be greater than 0, and the
// field may be a binary GUID, so only its length is checked. A request breaking
// either rule is answered 400 Bad Request ("could not be understood by the
// server due to malformed syntax") before any device state is written.
func TestQueryRefusesAMalformedDeviceID(t *testing.T) {
	ts := testServer(t)
	plain := func(id string) string {
		return "/Microsoft-Server-ActiveSync?Cmd=GetAttachment&User=" + testUser +
			"&DeviceId=" + url.QueryEscape(id) + "&DeviceType=iPhone"
	}
	for name, id := range map[string]string{
		"missing":        "",
		"33 characters":  strings.Repeat("a", 33),
		"a slash":        "dev/1",
		"a comma":        "dev,1",
		"a control byte": "dev\x001",
		"a space":        "dev 1",
	} {
		if resp, _ := do(t, ts, "POST", plain(id), "", true); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("plain DeviceId with %s: status %d, want 400", name, resp.StatusCode)
		}
	}
	if resp, _ := do(t, ts, "POST", plain(strings.Repeat("A9", 16)), "", true); resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("a 32-character alphanumeric DeviceId: status %d, want 501 (dispatch reached)", resp.StatusCode)
	}

	empty := []byte{0x0E, 0x04, 0x09, 0x04, 0x00, 0x00, 0x03, 'A', 'n', 'd'}
	if resp, _ := do(t, ts, "POST", "/Microsoft-Server-ActiveSync?"+base64.StdEncoding.EncodeToString(empty), "", true); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("base64 Device ID of length 0: status %d, want 400", resp.StatusCode)
	}
	guid := append([]byte{0x0E, 0x04, 0x09, 0x04, 0x10}, 0xff, 0xfe, 0x00, 0x01, 0x80, 0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89, 0x8a, 0x8b)
	guid = append(guid, 0x00, 0x03, 'A', 'n', 'd')
	if resp, _ := do(t, ts, "POST", "/Microsoft-Server-ActiveSync?"+base64.StdEncoding.EncodeToString(guid), "", true); resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("base64 Device ID that is a binary GUID: status %d, want 501 (dispatch reached)", resp.StatusCode)
	}
}

// TestBase64GUIDDeviceIDsKeepTheirOwnState proves two devices whose packed
// Device ID is a binary GUID (MS-ASHTTP 2.2.1.1.1.1 allows one) each keep their
// own sync state across requests. The state is stored as JSON, which cannot hold
// arbitrary bytes, so a raw GUID used as the key would change on the round trip
// and two GUIDs differing only in non-UTF-8 bytes would share one device.
func TestBase64GUIDDeviceIDsKeepTheirOwnState(t *testing.T) {
	ts, dir := seededServer(t)
	folderSync := func(guid []byte, key string) string {
		packed := append([]byte{0x0E, 0x09, 0x09, 0x04, byte(len(guid))}, guid...)
		packed = append(packed, 0x00, 0x03, 'A', 'n', 'd')
		body := wbxml.Marshal(wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHSyncKey, key)))
		req, err := http.NewRequest("POST", ts.URL+"/Microsoft-Server-ActiveSync?"+base64.StdEncoding.EncodeToString(packed), bytes.NewReader(body))
		mustNoErr(t, "build the request", err)
		req.SetBasicAuth(testUser, testPass)
		req.Header.Set("Content-Type", "application/vnd.ms-sync.wbxml")
		resp, err := http.DefaultClient.Do(req)
		mustNoErr(t, "send the request", err)
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		root, err := wbxml.Unmarshal(out)
		mustNoErr(t, "decode the reply", err)
		return root.ChildText(wbxml.FHStatus)
	}
	a := []byte{0xff, 0xfe, 0x01, 0x02, 0x80, 0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89, 0x8a, 0x8b}
	b := []byte{0xfe, 0xff, 0x01, 0x02, 0x80, 0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89, 0x8a, 0x8b}
	wantEq(t, "device a's prime", folderSync(a, "0"), "1")
	// A device that never synced holds no key 1, so it must be told to re-prime.
	wantEq(t, "device b's FolderSync before its own prime", folderSync(b, "1"), "9")
	wantEq(t, "device a's FolderSync with its key", folderSync(a, "1"), "1")

	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	defer st.Close()
	m, err := loadDevices(st)
	mustNoErr(t, "load the devices", err)
	wantEq(t, "recorded devices", len(m.Devices), 2)
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

// TestParseBase64QueryRefusesATruncatedPacket cuts a valid MS-ASHTTP 2.2.1.1.1.1
// packed query short at every field and lies about a length: each must be refused,
// never read past its end, and the live endpoint answers 400.
func TestParseBase64QueryRefusesATruncatedPacket(t *testing.T) {
	ts := testServer(t)
	full := []byte{0x0E, 0x09, 0x09, 0x04, 0x04, 'd', 'e', 'v', '1', 0x00, 0x03, 'A', 'n', 'd'}
	cases := map[string][]byte{
		"empty":                   {},
		"version only":            full[:1],
		"no device id length":     full[:4],
		"device id cut":           full[:7],
		"no policy key length":    full[:9],
		"no device type length":   full[:10],
		"device type cut":         full[:12],
		"device id length lies":   {0x0E, 0x09, 0x09, 0x04, 0xff, 'd', 'e', 'v'},
		"device type length lies": {0x0E, 0x09, 0x09, 0x04, 0x01, 'd', 0x00, 0xff, 'A'},
	}
	for name, b := range cases {
		enc := base64.StdEncoding.EncodeToString(b)
		if _, err := parseBase64Query(enc); err == nil {
			t.Errorf("%s: parsed, want a refusal", name)
		}
		if resp, _ := do(t, ts, "POST", "/Microsoft-Server-ActiveSync?"+enc, "", true); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, resp.StatusCode)
		}
	}
}

// TestAutodiscoverAnswersOnlyForTheCaller asks Autodiscover about another account:
// the answer names the authenticated user, never the address in the request, so a
// caller cannot learn settings or a display name for someone else.
func TestAutodiscoverAnswersOnlyForTheCaller(t *testing.T) {
	ts := testServer(t)
	body := `<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/mobilesync/requestschema/2006"><Request><EMailAddress>ceo@hermex.test</EMailAddress></Request></Autodiscover>`
	resp, out := do(t, ts, "POST", "/autodiscover/autodiscover.xml", body, true)
	if resp.StatusCode == http.StatusOK && strings.Contains(out, "ceo@hermex.test") {
		t.Errorf("autodiscover answered for another account:\n%s", out)
	}
	if resp.StatusCode == http.StatusOK && !strings.Contains(out, testUser) {
		t.Errorf("autodiscover answered without naming the caller:\n%s", out)
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

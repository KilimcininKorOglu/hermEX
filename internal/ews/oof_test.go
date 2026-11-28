package ews

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// oofServer builds a single-account EWS server and returns it with the account's
// mailbox path, so a test can inspect the store directly.
func oofServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := provisionedDir(t)
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

// oofTwoAccountServer builds a server with the caller (testUser) and a second
// account, returning the second account's mailbox path for the denial test.
func oofTwoAccountServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	aliceDir, bobDir := provisionedDir(t), provisionedDir(t)
	accs := directory.StaticAccounts{
		testUser:          {Password: testPass, MailboxPath: aliceDir},
		"bob@hermex.test": {Password: "bobsecret", MailboxPath: bobDir},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, bobDir
}

// getOofBody builds a GetUserOofSettings request targeting address.
func getOofBody(address string) string {
	return `<GetUserOofSettingsRequest xmlns="` + nsMessages + `">` +
		`<Mailbox xmlns="` + nsTypes + `"><Address>` + address + `</Address></Mailbox>` +
		`</GetUserOofSettingsRequest>`
}

// setOofBody builds a SetUserOofSettings request. An empty start/end omits the
// Duration element.
func setOofBody(address, state, audience, internal, external, start, end string) string {
	dur := ""
	if start != "" || end != "" {
		dur = `<Duration><StartTime>` + start + `</StartTime><EndTime>` + end + `</EndTime></Duration>`
	}
	return `<SetUserOofSettingsRequest xmlns="` + nsMessages + `">` +
		`<Mailbox xmlns="` + nsTypes + `"><Address>` + address + `</Address></Mailbox>` +
		`<UserOofSettings xmlns="` + nsTypes + `">` +
		`<OofState>` + state + `</OofState>` +
		`<ExternalAudience>` + audience + `</ExternalAudience>` +
		dur +
		`<InternalReply><Message>` + internal + `</Message></InternalReply>` +
		`<ExternalReply><Message>` + external + `</Message></ExternalReply>` +
		`</UserOofSettings></SetUserOofSettingsRequest>`
}

// parsedOof reads the OofSettings out of a GetUserOofSettings response by local
// name (namespaces are ignored on unmarshal).
type parsedOof struct {
	Code     string `xml:"Body>GetUserOofSettingsResponse>ResponseMessage>ResponseCode"`
	State    string `xml:"Body>GetUserOofSettingsResponse>OofSettings>OofState"`
	Audience string `xml:"Body>GetUserOofSettingsResponse>OofSettings>ExternalAudience"`
	Internal string `xml:"Body>GetUserOofSettingsResponse>OofSettings>InternalReply>Message"`
	External string `xml:"Body>GetUserOofSettingsResponse>OofSettings>ExternalReply>Message"`
	Start    string `xml:"Body>GetUserOofSettingsResponse>OofSettings>Duration>StartTime"`
	End      string `xml:"Body>GetUserOofSettingsResponse>OofSettings>Duration>EndTime"`
}

func getOof(t *testing.T, ts *httptest.Server, address string) parsedOof {
	t.Helper()
	resp, body := soapPost(t, ts, wrapRequest(getOofBody(address)), true)
	wantEq(t, "GetUserOofSettings HTTP status", resp.StatusCode, http.StatusOK)
	var p parsedOof
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse GetUserOofSettings response: %v\n%s", err, body)
	}
	return p
}

// TestOofEnabledRoundTrip proves a Set with OofState Enabled and an external
// audience round-trips through a Get with the replies intact.
func TestOofEnabledRoundTrip(t *testing.T) {
	ts, _ := oofServer(t)
	unauthResp, unauthBody := soapPost(t, ts, wrapRequest(getOofBody(testUser)), false)
	wantEq(t, "unauth GetUserOofSettings status", unauthResp.StatusCode, http.StatusUnauthorized)
	wantNotContains(t, "unauth GetUserOofSettings body", unauthBody, "OofSettings")

	resp, body := soapPost(t, ts, wrapRequest(
		setOofBody(testUser, "Enabled", "All", "internal text", "external text", "", "")), true)
	wantEq(t, "SetUserOofSettings HTTP status", resp.StatusCode, http.StatusOK)
	if !strings.Contains(body, "SetUserOofSettingsResponse") || !strings.Contains(body, `ResponseClass="Success"`) {
		t.Fatalf("set did not succeed: %s", body)
	}

	p := getOof(t, ts, testUser)
	if p.Code != "NoError" {
		t.Errorf("ResponseCode = %q, want NoError", p.Code)
	}
	if p.State != "Enabled" {
		t.Errorf("OofState = %q, want Enabled", p.State)
	}
	if p.Audience != "All" {
		t.Errorf("ExternalAudience = %q, want All", p.Audience)
	}
	if p.Internal != "internal text" || p.External != "external text" {
		t.Errorf("replies = %q / %q, want internal text / external text", p.Internal, p.External)
	}
}

// TestOofScheduledRoundTrip proves a Scheduled window round-trips: the state and
// the UTC Duration bounds survive a Set then Get.
func TestOofScheduledRoundTrip(t *testing.T) {
	ts, _ := oofServer(t)
	const start, end = "2026-07-01T09:00:00", "2026-07-10T17:00:00"
	resp, out := soapPost(t, ts, wrapRequest(
		setOofBody(testUser, "Scheduled", "Known", "i", "e", start, end)), true)
	wantEq(t, "scheduled SetUserOofSettings status", resp.StatusCode, http.StatusOK)
	wantContains(t, "scheduled SetUserOofSettings response", out, `ResponseClass="Success"`)

	p := getOof(t, ts, testUser)
	if p.State != "Scheduled" {
		t.Errorf("OofState = %q, want Scheduled", p.State)
	}
	if p.Audience != "Known" {
		t.Errorf("ExternalAudience = %q, want Known", p.Audience)
	}
	if p.Start != start || p.End != end {
		t.Errorf("Duration = %q..%q, want %q..%q", p.Start, p.End, start, end)
	}
}

// TestOofRefusesABrokenDuration sends Scheduled Sets whose Duration ends before it
// starts or carries a time that does not parse: each is refused with
// ErrorInvalidScheduledOofDuration and the stored OOF stays off, so a typo cannot
// leave an auto-reply running with no end.
func TestOofRefusesABrokenDuration(t *testing.T) {
	ts, dir := oofServer(t)
	for name, d := range map[string][2]string{
		"ends before it starts": {"2026-07-10T17:00:00", "2026-07-01T09:00:00"},
		"unparseable end":       {"2026-07-01T09:00:00", "next week"},
	} {
		resp, body := soapPost(t, ts, wrapRequest(setOofBody(testUser, "Scheduled", "All", "i", "e", d[0], d[1])), true)
		wantEq(t, name+" SetUserOofSettings status", resp.StatusCode, http.StatusOK)
		if !strings.Contains(body, "ErrorInvalidScheduledOofDuration") {
			t.Errorf("%s: response lacks ErrorInvalidScheduledOofDuration:\n%s", name, body)
		}
	}
	st, err := objectstore.OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustCloseStore(t, st)
	cfg, err := st.GetOOFSettings()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Errorf("a refused Duration was stored: %+v", cfg)
	}
}

// TestOofDisabled proves Set Disabled turns OOF off.
func TestOofDisabled(t *testing.T) {
	ts, _ := oofServer(t)
	// First enable, then disable, so the state visibly changes.
	r1, o1 := soapPost(t, ts, wrapRequest(setOofBody(testUser, "Enabled", "All", "x", "y", "", "")), true)
	wantEq(t, "enable OOF status", r1.StatusCode, http.StatusOK)
	wantContains(t, "enable OOF response", o1, `ResponseClass="Success"`)
	r2, o2 := soapPost(t, ts, wrapRequest(setOofBody(testUser, "Disabled", "None", "", "", "", "")), true)
	wantEq(t, "disable OOF status", r2.StatusCode, http.StatusOK)
	wantContains(t, "disable OOF response", o2, `ResponseClass="Success"`)

	p := getOof(t, ts, testUser)
	if p.State != "Disabled" {
		t.Errorf("OofState = %q, want Disabled", p.State)
	}
	if p.Audience != "None" {
		t.Errorf("ExternalAudience = %q, want None", p.Audience)
	}
}

// TestOofSubjectPreserved proves a Set (the wire carries no reply subject) does
// not wipe an admin-set subject: the stored InternalSubject survives while the
// reply body is updated.
func TestOofSubjectPreserved(t *testing.T) {
	ts, dir := oofServer(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetOOFSettings(objectstore.OOFSettings{InternalSubject: "Vacation", ExternalSubject: "Away"}); err != nil {
		mustCloseStore(t, st)
		t.Fatal(err)
	}
	mustCloseStore(t, st)

	resp, out := soapPost(t, ts, wrapRequest(
		setOofBody(testUser, "Enabled", "All", "new internal reply", "new external reply", "", "")), true)
	wantEq(t, "preserve-subject SetUserOofSettings status", resp.StatusCode, http.StatusOK)
	wantContains(t, "preserve-subject response", out, `ResponseClass="Success"`)

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := st2.GetOOFSettings()
	mustCloseStore(t, st2)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InternalSubject != "Vacation" || cfg.ExternalSubject != "Away" {
		t.Errorf("subjects wiped: internal %q external %q", cfg.InternalSubject, cfg.ExternalSubject)
	}
	if cfg.InternalReply != "new internal reply" {
		t.Errorf("InternalReply = %q, want the updated body", cfg.InternalReply)
	}
}

// TestOofForeignMailboxDenied is the OWASP A01 gate: a caller targeting another
// user's mailbox is denied (ErrorAccessDenied), no settings leak on Get, and the
// foreign store is never written on Set.
func TestOofForeignMailboxDenied(t *testing.T) {
	ts, bobDir := oofTwoAccountServer(t)

	// A Set targeting bob (authenticated as alice) is denied and writes nothing.
	setResp, setBody := soapPost(t, ts, wrapRequest(
		setOofBody("bob@hermex.test", "Enabled", "All", "evil", "evil", "", "")), true)
	wantEq(t, "foreign SetUserOofSettings status", setResp.StatusCode, http.StatusOK)
	if !strings.Contains(setBody, `ResponseClass="Error"`) || !strings.Contains(setBody, "ErrorAccessDenied") {
		t.Errorf("Set for a foreign mailbox not denied: %s", setBody)
	}
	st, err := objectstore.Open(bobDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetOOFSettings()
	mustCloseStore(t, st)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Error("a foreign mailbox's OOF was written despite the denial")
	}

	// A Get targeting bob is denied and leaks no OofSettings.
	getResp, getBody := soapPost(t, ts, wrapRequest(getOofBody("bob@hermex.test")), true)
	wantEq(t, "foreign GetUserOofSettings status", getResp.StatusCode, http.StatusOK)
	if !strings.Contains(getBody, "ErrorAccessDenied") {
		t.Errorf("Get for a foreign mailbox not denied: %s", getBody)
	}
	if strings.Contains(getBody, "<OofSettings") {
		t.Errorf("Get for a foreign mailbox leaked OofSettings: %s", getBody)
	}
}

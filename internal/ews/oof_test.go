package ews

import (
	"encoding/xml"
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
	dir := t.TempDir()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

// oofTwoAccountServer builds a server with the caller (testUser) and a second
// account, returning the second account's mailbox path for the denial test.
func oofTwoAccountServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	aliceDir, bobDir := t.TempDir(), t.TempDir()
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
	_, body := soapPost(t, ts, wrapRequest(getOofBody(address)), true)
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
	_, body := soapPost(t, ts, wrapRequest(
		setOofBody(testUser, "Enabled", "All", "internal text", "external text", "", "")), true)
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
	soapPost(t, ts, wrapRequest(
		setOofBody(testUser, "Scheduled", "Known", "i", "e", start, end)), true)

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

// TestOofDisabled proves Set Disabled turns OOF off.
func TestOofDisabled(t *testing.T) {
	ts, _ := oofServer(t)
	// First enable, then disable, so the state visibly changes.
	soapPost(t, ts, wrapRequest(setOofBody(testUser, "Enabled", "All", "x", "y", "", "")), true)
	soapPost(t, ts, wrapRequest(setOofBody(testUser, "Disabled", "None", "", "", "", "")), true)

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
		t.Fatal(err)
	}
	st.Close()

	soapPost(t, ts, wrapRequest(
		setOofBody(testUser, "Enabled", "All", "new internal reply", "new external reply", "", "")), true)

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := st2.GetOOFSettings()
	st2.Close()
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
	_, setBody := soapPost(t, ts, wrapRequest(
		setOofBody("bob@hermex.test", "Enabled", "All", "evil", "evil", "", "")), true)
	if !strings.Contains(setBody, `ResponseClass="Error"`) || !strings.Contains(setBody, "ErrorAccessDenied") {
		t.Errorf("Set for a foreign mailbox not denied: %s", setBody)
	}
	st, err := objectstore.Open(bobDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := st.GetOOFSettings()
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Error("a foreign mailbox's OOF was written despite the denial")
	}

	// A Get targeting bob is denied and leaks no OofSettings.
	_, getBody := soapPost(t, ts, wrapRequest(getOofBody("bob@hermex.test")), true)
	if !strings.Contains(getBody, "ErrorAccessDenied") {
		t.Errorf("Get for a foreign mailbox not denied: %s", getBody)
	}
	if strings.Contains(getBody, "<OofSettings") {
		t.Errorf("Get for a foreign mailbox leaked OofSettings: %s", getBody)
	}
}

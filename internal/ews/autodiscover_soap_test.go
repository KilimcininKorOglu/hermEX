package ews

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// adNS is the SOAP Autodiscover namespace. The parse structs below qualify every
// element with it, so a response that emitted the elements in the wrong namespace
// (which a bare-local-name parser would still accept) fails the test.
const adNS = "http://schemas.microsoft.com/exchange/2010/Autodiscover"

// adReplyEnvelope parses the SOAP envelope wrapping a GetUserSettingsResponseMessage.
// The soap and autodiscover namespaces are fully qualified so a wrong-namespace
// emission (which a bare-local-name parser would still accept) fails the test.
type adReplyEnvelope struct {
	XMLName xml.Name `xml:"http://schemas.xmlsoap.org/soap/envelope/ Envelope"`
	Body    struct {
		Reply userSettingsReply `xml:"http://schemas.microsoft.com/exchange/2010/Autodiscover GetUserSettingsResponseMessage"`
	} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
}

// userSettingsReply is a fully namespace-qualified parse of a
// GetUserSettingsResponseMessage, used to assert the real settings reach the client
// under the autodiscover namespace.
type userSettingsReply struct {
	Response struct {
		ErrorCode     string `xml:"http://schemas.microsoft.com/exchange/2010/Autodiscover ErrorCode"`
		UserResponses struct {
			UserResponse []struct {
				ErrorCode    string `xml:"http://schemas.microsoft.com/exchange/2010/Autodiscover ErrorCode"`
				UserSettings struct {
					UserSetting []struct {
						Name  string `xml:"http://schemas.microsoft.com/exchange/2010/Autodiscover Name"`
						Value string `xml:"http://schemas.microsoft.com/exchange/2010/Autodiscover Value"`
					} `xml:"http://schemas.microsoft.com/exchange/2010/Autodiscover UserSetting"`
				} `xml:"http://schemas.microsoft.com/exchange/2010/Autodiscover UserSettings"`
			} `xml:"http://schemas.microsoft.com/exchange/2010/Autodiscover UserResponse"`
		} `xml:"http://schemas.microsoft.com/exchange/2010/Autodiscover UserResponses"`
	} `xml:"http://schemas.microsoft.com/exchange/2010/Autodiscover Response"`
}

// settingValue returns the value of the named setting in the first UserResponse, or
// "" when absent.
func (r userSettingsReply) settingValue(name string) string {
	if len(r.Response.UserResponses.UserResponse) == 0 {
		return ""
	}
	for _, s := range r.Response.UserResponses.UserResponse[0].UserSettings.UserSetting {
		if s.Name == name {
			return s.Value
		}
	}
	return ""
}

// adSoapPost POSTs a SOAP Autodiscover envelope to the .svc endpoint.
func adSoapPost(t *testing.T, ts *httptest.Server, body string, auth bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/autodiscover/autodiscover.svc", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	if auth {
		req.SetBasicAuth(testUser, testPass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(data)
}

// userSettingsEnvelope wraps a GetUserSettings request naming a mailbox and the
// settings to return.
func userSettingsEnvelope(mailbox string, settings ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:a="`)
	b.WriteString(nsAutodiscover)
	b.WriteString(`" xmlns:soap="`)
	b.WriteString(nsSOAP)
	b.WriteString(`"><soap:Body><a:GetUserSettingsRequestMessage><a:Request>`)
	b.WriteString(`<a:Users><a:User><a:Mailbox>`)
	b.WriteString(mailbox)
	b.WriteString(`</a:Mailbox></a:User></a:Users><a:RequestedSettings>`)
	for _, s := range settings {
		b.WriteString(`<a:Setting>`)
		b.WriteString(s)
		b.WriteString(`</a:Setting>`)
	}
	b.WriteString(`</a:RequestedSettings></a:Request></a:GetUserSettingsRequestMessage></soap:Body></soap:Envelope>`)
	return b.String()
}

// TestAutodiscoverSOAPGetUserSettings proves the SOAP Autodiscover endpoint returns
// the EWS URL and the caller's identity under the autodiscover namespace, parsed
// here with fully namespace-qualified tags so a wrong-namespace emission would fail.
func TestAutodiscoverSOAPGetUserSettings(t *testing.T) {
	ts := newTestServer(t)
	body := userSettingsEnvelope(testUser, "ExternalEwsUrl", "InternalEwsUrl", "UserDisplayName", "AutoDiscoverSMTPAddress")
	resp, out := adSoapPost(t, ts, body, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, out)
	}

	var env adReplyEnvelope
	if err := xml.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse response: %v\n%s", err, out)
	}
	reply := env.Body.Reply
	if len(reply.Response.UserResponses.UserResponse) != 1 {
		t.Fatalf("want exactly one UserResponse, got %d: %s", len(reply.Response.UserResponses.UserResponse), out)
	}
	if reply.Response.ErrorCode != "NoError" {
		t.Errorf("Response ErrorCode = %q, want NoError", reply.Response.ErrorCode)
	}
	if reply.Response.UserResponses.UserResponse[0].ErrorCode != "NoError" {
		t.Errorf("UserResponse ErrorCode = %q, want NoError", reply.Response.UserResponses.UserResponse[0].ErrorCode)
	}

	wantURL := "https://mail.hermex.test/EWS/Exchange.asmx"
	if got := reply.settingValue("ExternalEwsUrl"); got != wantURL {
		t.Errorf("ExternalEwsUrl = %q, want %q", got, wantURL)
	}
	if got := reply.settingValue("InternalEwsUrl"); got != wantURL {
		t.Errorf("InternalEwsUrl = %q, want %q", got, wantURL)
	}
	if got := reply.settingValue("UserDisplayName"); got != testUser {
		t.Errorf("UserDisplayName = %q, want %q", got, testUser)
	}
	if got := reply.settingValue("AutoDiscoverSMTPAddress"); got != testUser {
		t.Errorf("AutoDiscoverSMTPAddress = %q, want %q", got, testUser)
	}
}

// TestAutodiscoverSOAPOmitsUnknownSetting proves a requested setting hermEX has no
// value for is omitted from the response rather than answered with an empty or
// fabricated value.
func TestAutodiscoverSOAPOmitsUnknownSetting(t *testing.T) {
	ts := newTestServer(t)
	resp, out := adSoapPost(t, ts, userSettingsEnvelope(testUser, "ExternalEwsUrl", "UserDN"), true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, out)
	}
	var env adReplyEnvelope
	if err := xml.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse response: %v\n%s", err, out)
	}
	reply := env.Body.Reply
	if got := reply.settingValue("ExternalEwsUrl"); got == "" {
		t.Error("ExternalEwsUrl missing, want the EWS URL")
	}
	if got := reply.settingValue("UserDN"); got != "" {
		t.Errorf("UserDN = %q, want it omitted (hermEX has no AD distinguished name)", got)
	}
}

// TestAutodiscoverSOAPRequiresAuth proves the SOAP Autodiscover endpoint challenges
// an unauthenticated request, so settings are not disclosed without credentials.
func TestAutodiscoverSOAPRequiresAuth(t *testing.T) {
	ts := newTestServer(t)
	resp, _ := adSoapPost(t, ts, userSettingsEnvelope(testUser, "ExternalEwsUrl"), false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("missing WWW-Authenticate challenge")
	}
}

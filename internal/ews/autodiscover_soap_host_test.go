package ews

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// hostlessServer serves Autodiscover with no configured hostname, the deployment
// that falls back to the request's own Host header.
func hostlessServer(t *testing.T) *httptest.Server {
	t.Helper()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: provisionedDir(t)}}
	ts := httptest.NewServer(NewServer(accs, accs, "").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// adSoapPostHostWithAuth is adSoapPost with an explicit Host header and auth flag.
func adSoapPostHostWithAuth(t *testing.T, ts *httptest.Server, host, body string, auth bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/autodiscover/autodiscover.svc", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Host = host
	if auth {
		req.SetBasicAuth(testUser, testPass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	closeBody(t, resp)
	return resp, string(data)
}

// adSoapPostHost is adSoapPost with an explicit Host header, which an HTTP/1.1
// client fully controls.
func adSoapPostHost(t *testing.T, ts *httptest.Server, host, body string) (*http.Response, string) {
	t.Helper()
	return adSoapPostHostWithAuth(t, ts, host, body, true)
}

// TestSOAPAutodiscoverRefusesAnUntrustworthyHostHeader proves the SOAP surface
// applies the same guard its POX and ActiveSync siblings apply. The response
// carries the EWS URL the client binds its whole session to, so a client-supplied
// Host must never reach it unchecked: an unconfigured deployment would otherwise
// hand every caller an attacker-chosen address.
func TestSOAPAutodiscoverRefusesAnUntrustworthyHostHeader(t *testing.T) {
	ts := hostlessServer(t)
	body := userSettingsEnvelope(testUser, "ExternalEwsUrl", "InternalEwsUrl")

	for _, host := range []string{"evil", "127.0.0.1", "10.0.0.5:443", "under_score.example"} {
		resp, out := adSoapPostHost(t, ts, host, body)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("Host %q = %d, want 503; body=%s", host, resp.StatusCode, out)
		}
		if strings.Contains(out, host) {
			t.Errorf("Host %q was reflected into the response: %s", host, out)
		}
	}
}

// TestSOAPAutodiscoverAcceptsAPublicHostHeader is the negative control: a
// multi-tenant deployment with no configured hostname still answers when the
// request carries a plausible public FQDN, so the guard refuses only what it
// should.
func TestSOAPAutodiscoverAcceptsAPublicHostHeader(t *testing.T) {
	ts := hostlessServer(t)
	body := userSettingsEnvelope(testUser, "ExternalEwsUrl", "InternalEwsUrl")

	resp, out := adSoapPostHost(t, ts, "mail.tenant.example", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, out)
	}
	var env adReplyEnvelope
	if err := xml.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse response: %v\n%s", err, out)
	}
	want := "https://mail.tenant.example/EWS/Exchange.asmx"
	if got := env.Body.Reply.settingValue("ExternalEwsUrl"); got != want {
		t.Errorf("ExternalEwsUrl = %q, want %q", got, want)
	}
}

// TestSOAPAutodiscoverPrefersTheConfiguredHostname proves the configured
// hostname stays authoritative: a spoofed Host header cannot override it.
func TestSOAPAutodiscoverPrefersTheConfiguredHostname(t *testing.T) {
	ts := newTestServer(t) // configured as mail.hermex.test
	body := userSettingsEnvelope(testUser, "ExternalEwsUrl")

	unauthResp, unauthOut := adSoapPostHostWithAuth(t, ts, "attacker.example", body, false)
	wantEq(t, "unauth SOAP autodiscover status", unauthResp.StatusCode, http.StatusUnauthorized)
	wantNotContains(t, "unauth SOAP autodiscover body", unauthOut, "Exchange.asmx")

	resp, out := adSoapPostHost(t, ts, "attacker.example", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, out)
	}
	if strings.Contains(out, "attacker.example") {
		t.Errorf("the spoofed Host overrode the configured hostname: %s", out)
	}
	wantContains(t, "authoritative ExternalEwsUrl", out, "https://mail.hermex.test/EWS/Exchange.asmx")
}

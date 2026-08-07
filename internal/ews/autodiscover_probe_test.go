package ews

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// autodiscoverProbe posts to the Outlook Autodiscover endpoint, optionally
// authenticated, and returns the response and its body.
func autodiscoverProbe(t *testing.T, url string, authed bool, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/autodiscover/autodiscover.xml", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if authed {
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

// TestAutodiscoverProbeRevealsNothingUnauthenticated pins the property that makes
// this endpoint safe to expose: it authenticates before it does anything, so a
// stranger probing it cannot read the server hostname, the EWS endpoint, the
// supported protocols, or whether any address exists. Only the challenge comes
// back. Without this, repeated probing would be a reconnaissance channel.
func TestAutodiscoverProbeRevealsNothingUnauthenticated(t *testing.T) {
	ts := newTestServer(t)
	resp, body := autodiscoverProbe(t, ts.URL, false, "<Autodiscover/>")

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("missing WWW-Authenticate challenge")
	}
	for _, leak := range []string{"mail.hermex.test", "Exchange.asmx", "AuthPackage", testUser} {
		if strings.Contains(body, leak) {
			t.Errorf("an unauthenticated probe disclosed %q: %s", leak, body)
		}
	}
}

// TestAutodiscoverEchoesTheAuthenticatedIdentityOnly proves the reply is built
// from who the caller proved to be, never from what they asked for. A request
// body naming another address must not make the server confirm that address
// exists or answer on its behalf.
func TestAutodiscoverEchoesTheAuthenticatedIdentityOnly(t *testing.T) {
	ts := newTestServer(t)
	const probed = "victim@hermex.test"
	resp, body := autodiscoverProbe(t, ts.URL, true,
		`<Autodiscover><Request><EMailAddress>`+probed+`</EMailAddress></Request></Autodiscover>`)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(body, probed) {
		t.Errorf("the reply echoed the requested address rather than the authenticated one: %s", body)
	}
	if !strings.Contains(body, testUser) {
		t.Errorf("the reply does not carry the authenticated identity: %s", body)
	}
}

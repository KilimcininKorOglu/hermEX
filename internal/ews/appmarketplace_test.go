package ews

import (
	"strings"
	"testing"
)

// TestGetAppMarketplaceURLIsAnswered keeps a connecting desktop client from
// reading the server as broken. It asks for the add-in store on connect, and the
// unsupported-operation fault says "invalid request" rather than "no add-ins
// here", so the client retries it every time.
func TestGetAppMarketplaceURLIsAnswered(t *testing.T) {
	ts, _ := seededEWS(t)
	req := wrapRequest(`<GetAppMarketplaceUrl xmlns="` + nsMessages + `"/>`)
	resp, out := soapPost(t, ts, req, true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d: %s", resp.StatusCode, out)
	}
	if strings.Contains(out, "ErrorInvalidRequest") {
		t.Fatalf("the operation is still unsupported: %s", out)
	}
	if !strings.Contains(out, "GetAppMarketplaceUrlResponse") {
		t.Errorf("no response element: %s", out)
	}
	if !strings.Contains(out, "<ResponseCode>NoError</ResponseCode>") {
		t.Errorf("not a NoError answer: %s", out)
	}
	// The URL is empty: this server hosts no add-in store, and that is what an
	// empty one says.
	if !strings.Contains(out, "<AppMarketplaceUrl></AppMarketplaceUrl>") {
		t.Errorf("no empty marketplace url: %s", out)
	}
}

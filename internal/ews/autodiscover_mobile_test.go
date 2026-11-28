package ews

import (
	"net/http"
	"strings"
	"testing"
)

const mobileSyncRequest = `<?xml version="1.0" encoding="utf-8"?>
<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/mobilesync/requestschema/2006">
<Request><EMailAddress>victim@hermex.test</EMailAddress>
<AcceptableResponseSchema>http://schemas.microsoft.com/exchange/autodiscover/mobilesync/responseschema/2006</AcceptableResponseSchema>
</Request></Autodiscover>`

// [MS-ASCMD] 2.2.3.1: a device that asks for the mobilesync response schema
// gets a MobileSync server URL pointing at the ActiveSync endpoint, carrying
// the authenticated address, not the one the body named.
func TestAutodiscoverAnswersMobileSyncSchema(t *testing.T) {
	ts := newTestServer(t)
	unauthResp, unauthBody := autodiscoverProbe(t, ts.URL, false, mobileSyncRequest)
	wantEq(t, "unauthenticated mobile status", unauthResp.StatusCode, http.StatusUnauthorized)
	wantNotContains(t, "unauthenticated mobile body", unauthBody, "MobileSync")
	wantNotContains(t, "unauthenticated mobile body", unauthBody, testUser)

	resp, body := autodiscoverProbe(t, ts.URL, true, mobileSyncRequest)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{
		"mobilesync/responseschema/2006",
		"<Type>MobileSync</Type>",
		"<Url>https://mail.hermex.test/Microsoft-Server-ActiveSync</Url>",
		"<EMailAddress>" + testUser + "</EMailAddress>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("reply lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "victim@") || strings.Contains(body, "EwsUrl") {
		t.Fatalf("mobile reply leaked the probed address or the Outlook schema: %s", body)
	}
}

// An Outlook request keeps the Outlook schema.
func TestAutodiscoverOutlookSchemaStaysOutlook(t *testing.T) {
	ts := newTestServer(t)
	resp, body := autodiscoverProbe(t, ts.URL, true, strings.Replace(mobileSyncRequest, "mobilesync/responseschema", "outlook/responseschema", 1))
	wantEq(t, "outlook status", resp.StatusCode, http.StatusOK)
	if !strings.Contains(body, "outlook/responseschema/2006a") || strings.Contains(body, "MobileSync") {
		t.Fatalf("outlook request got %s", body)
	}
}

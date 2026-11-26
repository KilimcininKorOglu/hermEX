package ews

import (
	"encoding/json"
	"net/http"
	"testing"
)

func adJSONGet(t *testing.T, url string) (int, map[string]string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	if resp.Header.Get("Content-Type") == "application/json; charset=utf-8" {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

// Autodiscover v2: GET .../autodiscover.json/v1.0/<address>?Protocol=X answers
// {"Protocol":X,"Url":<endpoint>} without authentication, for EWS, ActiveSync
// and AutodiscoverV1, in any casing of the path and the protocol name.
func TestAutodiscoverJSONNamesProtocolEndpoints(t *testing.T) {
	ts := newTestServer(t)
	cases := []struct{ target, proto, path string }{
		{"/autodiscover/autodiscover.json/v1.0/someone@hermex.test?Protocol=EWS", "EWS", "/EWS/Exchange.asmx"},
		{"/Autodiscover/Autodiscover.json/v1.0/someone@hermex.test?Protocol=activesync", "ActiveSync", "/Microsoft-Server-ActiveSync"},
		{"/autodiscover/autodiscover.json?Email=someone@hermex.test&Protocol=AutodiscoverV1", "AutodiscoverV1", "/autodiscover/autodiscover.xml"},
	}
	for _, c := range cases {
		status, body := adJSONGet(t, ts.URL+c.target)
		if status != http.StatusOK || body["Protocol"] != c.proto || body["Url"] != "https://mail.hermex.test"+c.path {
			t.Fatalf("%s: status %d body %v", c.target, status, body)
		}
	}
}

// An unknown or missing protocol is refused with InvalidProtocol, never
// answered with some default endpoint.
func TestAutodiscoverJSONRefusesUnknownProtocol(t *testing.T) {
	ts := newTestServer(t)
	for _, q := range []string{"?Protocol=Rest", "?Protocol=", ""} {
		status, body := adJSONGet(t, ts.URL+"/autodiscover/autodiscover.json/v1.0/x@hermex.test"+q)
		if status != http.StatusBadRequest || body["ErrorCode"] != "InvalidProtocol" || body["Url"] != "" {
			t.Fatalf("%q: status %d body %v", q, status, body)
		}
	}
}

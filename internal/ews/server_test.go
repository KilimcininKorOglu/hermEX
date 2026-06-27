package ews

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

const (
	testUser = "alice@hermex.test"
	testPass = "secret"
)

// newTestServer builds an EWS server backed by a single static test account.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// wrapRequest wraps an operation element in a minimal SOAP request envelope.
func wrapRequest(inner string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="` + nsSOAP + `">` +
		`<soap:Body>` + inner + `</soap:Body></soap:Envelope>`
}

// soapPost POSTs a SOAP body to the EWS endpoint, optionally authenticated.
func soapPost(t *testing.T, ts *httptest.Server, body string, auth bool) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/EWS/Exchange.asmx", strings.NewReader(body))
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

// TestRequiresAuth confirms an unauthenticated request gets a 401 challenge.
func TestRequiresAuth(t *testing.T) {
	ts := newTestServer(t)
	resp, _ := soapPost(t, ts, wrapRequest(`<GetFolder xmlns="`+nsMessages+`"/>`), false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("missing WWW-Authenticate challenge")
	}
}

// TestOptions confirms OPTIONS advertises POST.
func TestOptions(t *testing.T) {
	ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/EWS/Exchange.asmx", nil)
	req.SetBasicAuth(testUser, testPass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Allow"), "POST") {
		t.Errorf("Allow = %q, want to contain POST", resp.Header.Get("Allow"))
	}
}

// TestReadEnvelope confirms the operation name + inner element are extracted
// from a SOAP request envelope.
func TestReadEnvelope(t *testing.T) {
	body := wrapRequest(`<GetFolder xmlns="` + nsMessages + `"><FolderShape/></GetFolder>`)
	r := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	op, inner, _, err := readEnvelope(r)
	if err != nil {
		t.Fatal(err)
	}
	if op != "GetFolder" {
		t.Errorf("op = %q, want GetFolder", op)
	}
	if !strings.Contains(string(inner), "FolderShape") {
		t.Errorf("inner = %q, want to contain FolderShape", inner)
	}
}

// TestReadEnvelopeRespectsBodyLimit proves the request-body cap is read live from the
// operator-set value, so an edit (applied by the EWS daemon's poll) decides what is
// accepted: a body over the cap is truncated and fails to parse, and restoring the
// default admits the same envelope, with no restart.
func TestReadEnvelopeRespectsBodyLimit(t *testing.T) {
	body := wrapRequest(`<GetFolder xmlns="` + nsMessages + `"><FolderShape/></GetFolder>`)

	SetMaxRequestBody(16) // far smaller than the envelope
	defer SetMaxRequestBody(0)
	r := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	if _, _, _, err := readEnvelope(r); err == nil {
		t.Error("envelope over the 16-byte cap parsed, want a truncation/parse error")
	}

	// Restoring the default (0) admits the same envelope.
	SetMaxRequestBody(0)
	r2 := httptest.NewRequest(http.MethodPost, "/EWS/Exchange.asmx", strings.NewReader(body))
	if op, _, _, err := readEnvelope(r2); err != nil || op != "GetFolder" {
		t.Errorf("envelope under the default cap = op %q err %v, want GetFolder / nil", op, err)
	}
}

// TestFirstElementName confirms the first element local name is found and that
// an empty fragment yields "".
func TestFirstElementName(t *testing.T) {
	if got := firstElementName([]byte(`<m:Foo xmlns:m="x"><Bar/></m:Foo>`)); got != "Foo" {
		t.Errorf("name = %q, want Foo", got)
	}
	if got := firstElementName([]byte("   ")); got != "" {
		t.Errorf("name = %q, want empty", got)
	}
}

// TestUnsupportedOperation confirms an operation outside the v1 surface
// (GetSearchableMailboxes is an eDiscovery operation the per-mailbox model does
// not serve) returns a SOAP Fault carrying an EWS response code.
func TestUnsupportedOperation(t *testing.T) {
	ts := newTestServer(t)
	resp, body := soapPost(t, ts, wrapRequest(`<GetSearchableMailboxes xmlns="`+nsMessages+`"/>`), true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if !strings.Contains(body, "Fault") || !strings.Contains(body, "ErrorInvalidRequest") {
		t.Errorf("expected a SOAP Fault with ErrorInvalidRequest, got: %s", body)
	}
}

// TestMalformedEnvelope confirms a non-SOAP body returns a Fault, not a panic.
func TestMalformedEnvelope(t *testing.T) {
	ts := newTestServer(t)
	resp, body := soapPost(t, ts, "this is not xml", true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if !strings.Contains(body, "Fault") {
		t.Errorf("expected a SOAP Fault, got: %s", body)
	}
}

// TestAutodiscover confirms the Outlook Autodiscover response carries the EWS URL
// and advertises Outlook Anywhere (RPC/HTTP) so a desktop Outlook would attempt
// the /rpc/rpcproxy.dll endpoint.
func TestAutodiscover(t *testing.T) {
	ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/autodiscover/autodiscover.xml", strings.NewReader("<Autodiscover/>"))
	req.SetBasicAuth(testUser, testPass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	body := string(data)
	if !strings.Contains(body, "https://mail.hermex.test/EWS/Exchange.asmx") {
		t.Errorf("autodiscover missing EwsUrl, got: %s", body)
	}
	if !strings.Contains(body, "outlook/responseschema/2006a") {
		t.Errorf("autodiscover missing Outlook schema, got: %s", body)
	}
	// Outlook Anywhere (RPC/HTTP) markers: the EXPR provider must carry the
	// HTTP-only connect directive and the auth package the transport accepts.
	for _, want := range []string{"<ServerExclusiveConnect>On</ServerExclusiveConnect>", "<AuthPackage>Basic</AuthPackage>"} {
		if !strings.Contains(body, want) {
			t.Errorf("autodiscover missing RPC/HTTP marker %q, got: %s", want, body)
		}
	}
}

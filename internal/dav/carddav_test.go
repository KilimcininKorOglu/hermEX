package dav

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doFull issues a request with a body and arbitrary headers.
func doFull(t *testing.T, ts *httptest.Server, method, path, body string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, testPass)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(rb)
}

const adaVCard = "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ada Lovelace\r\nEMAIL:ada@analytical.test\r\nUID:ada-1\r\nEND:VCARD\r\n"

func contactURL(name string) string {
	return "/dav/addressbooks/" + testUser + "/contacts/" + name
}

// TestPutGetRoundTrip stores a vCard with PUT and reads it back with GET,
// confirming the contact survives conversion to MAPI and back.
func TestPutGetRoundTrip(t *testing.T) {
	ts := davServer(t)
	url := contactURL("ada.vcf")

	resp, body := doFull(t, ts, "PUT", url, adaVCard, map[string]string{"Content-Type": "text/vcard"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status %d, want 201\n%s", resp.StatusCode, body)
	}
	if resp.Header.Get("ETag") == "" {
		t.Error("PUT did not return an ETag")
	}

	resp, body = doFull(t, ts, "GET", url, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/vcard") {
		t.Errorf("GET content-type %q", ct)
	}
	for _, want := range []string{"BEGIN:VCARD", "FN:Ada Lovelace", "ada@analytical.test", "END:VCARD"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET body missing %q\n%s", want, body)
		}
	}

	// The chosen resource name round-trips into the collection listing.
	_, pf := doFull(t, ts, "PROPFIND", contactURL(""), "", map[string]string{"Depth": "1"})
	if !strings.Contains(pf, "ada.vcf") {
		t.Errorf("PROPFIND lacks the PUT resource name ada.vcf\n%s", pf)
	}
}

// TestPutReplace confirms a second PUT to the same URL replaces (204), not
// duplicates.
func TestPutReplace(t *testing.T) {
	ts := davServer(t)
	url := contactURL("ada.vcf")
	doFull(t, ts, "PUT", url, adaVCard, nil)

	updated := strings.Replace(adaVCard, "Ada Lovelace", "Ada A. Lovelace", 1)
	resp, body := doFull(t, ts, "PUT", url, updated, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("replace PUT status %d, want 204\n%s", resp.StatusCode, body)
	}
	_, get := doFull(t, ts, "GET", url, "", nil)
	if !strings.Contains(get, "Ada A. Lovelace") {
		t.Errorf("replace did not take effect\n%s", get)
	}
	// Still exactly one object.
	_, pf := doFull(t, ts, "PROPFIND", contactURL(""), "", map[string]string{"Depth": "1"})
	if n := strings.Count(pf, ".vcf"); n != 1 {
		t.Errorf("after replace got %d objects, want 1", n)
	}
}

// TestIfNoneMatchCreateOnly confirms If-None-Match: * makes PUT create-only.
func TestIfNoneMatchCreateOnly(t *testing.T) {
	ts := davServer(t)
	url := contactURL("ada.vcf")
	doFull(t, ts, "PUT", url, adaVCard, nil)
	resp, _ := doFull(t, ts, "PUT", url, adaVCard, map[string]string{"If-None-Match": "*"})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("If-None-Match on existing: status %d, want 412", resp.StatusCode)
	}
}

// TestIfMatchConflict confirms a stale If-Match is rejected with 412.
func TestIfMatchConflict(t *testing.T) {
	ts := davServer(t)
	url := contactURL("ada.vcf")
	doFull(t, ts, "PUT", url, adaVCard, nil)
	resp, _ := doFull(t, ts, "PUT", url, adaVCard, map[string]string{"If-Match": `"99999"`})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("stale If-Match: status %d, want 412", resp.StatusCode)
	}
}

// TestDelete removes a contact and confirms it is then absent.
func TestDelete(t *testing.T) {
	ts := davServer(t)
	url := contactURL("ada.vcf")
	doFull(t, ts, "PUT", url, adaVCard, nil)

	resp, _ := doFull(t, ts, "DELETE", url, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status %d, want 204", resp.StatusCode)
	}
	resp, _ = doFull(t, ts, "GET", url, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after delete: status %d, want 404", resp.StatusCode)
	}
}

// TestMkColAddressbook confirms extended MKCOL creates a usable second address book
// and that its objects are isolated from the default Contacts collection (RFC 5689).
func TestMkColAddressbook(t *testing.T) {
	ts := davServer(t)
	team := "/dav/addressbooks/" + testUser + "/team/"
	mkcol := `<d:mkcol xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">` +
		`<d:set><d:prop><d:resourcetype><d:collection/><card:addressbook/></d:resourcetype></d:prop></d:set></d:mkcol>`

	resp, _ := doFull(t, ts, "MKCOL", team, mkcol, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL status %d, want 201", resp.StatusCode)
	}
	resp, _ = doFull(t, ts, "PUT", team+"ada.vcf", adaVCard, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT into new address book: status %d, want 201", resp.StatusCode)
	}
	resp, body := doFull(t, ts, "GET", team+"ada.vcf", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Ada Lovelace") {
		t.Errorf("GET from new address book: status %d\n%s", resp.StatusCode, body)
	}
	// The contact must not leak into the well-known Contacts collection.
	if resp, _ := doFull(t, ts, "GET", contactURL("ada.vcf"), "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("contact leaked into the default address book: status %d, want 404", resp.StatusCode)
	}
}

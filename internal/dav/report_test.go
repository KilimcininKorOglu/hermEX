package dav

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

const bobVCard = "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Bob Bits\r\nEMAIL:bob@bits.test\r\nUID:bob-1\r\nEND:VCARD\r\n"

var syncTokenRE = regexp.MustCompile(`hermex:sync:\d+`)

// TestReportMultiget requests two named contacts and confirms both come back
// with their address-data.
func TestReportMultiget(t *testing.T) {
	ts := davServer(t)
	doFull(t, ts, "PUT", contactURL("ada.vcf"), adaVCard, nil)
	doFull(t, ts, "PUT", contactURL("bob.vcf"), bobVCard, nil)

	body := `<card:addressbook-multiget xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">
  <d:prop><d:getetag/><card:address-data/></d:prop>
  <d:href>` + contactURL("ada.vcf") + `</d:href>
  <d:href>` + contactURL("bob.vcf") + `</d:href>
</card:addressbook-multiget>`
	resp, out := doFull(t, ts, "REPORT", contactURL(""), body, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, out)
	}
	for _, want := range []string{"Ada Lovelace", "Bob Bits", "getetag"} {
		if !strings.Contains(out, want) {
			t.Errorf("multiget missing %q\n%s", want, out)
		}
	}
}

// TestReportQuery returns every member's address-data.
func TestReportQuery(t *testing.T) {
	ts := davServer(t)
	doFull(t, ts, "PUT", contactURL("ada.vcf"), adaVCard, nil)
	body := `<card:addressbook-query xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">
  <d:prop><d:getetag/><card:address-data/></d:prop>
  <card:filter/>
</card:addressbook-query>`
	resp, out := doFull(t, ts, "REPORT", contactURL(""), body, map[string]string{"Depth": "1"})
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d, want 207\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "Ada Lovelace") {
		t.Errorf("query missing the member\n%s", out)
	}
}

// TestReportSyncCollection checks incremental sync: an initial sync returns all
// members and a token; after a new PUT, a sync with that token returns only the
// new member.
func TestReportSyncCollection(t *testing.T) {
	ts := davServer(t)
	doFull(t, ts, "PUT", contactURL("ada.vcf"), adaVCard, nil)

	initial := `<d:sync-collection xmlns:d="DAV:"><d:sync-token/><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	resp, out := doFull(t, ts, "REPORT", contactURL(""), initial, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("initial sync status %d, want 207\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "Ada Lovelace") {
		t.Errorf("initial sync missing existing member\n%s", out)
	}
	token := syncTokenRE.FindString(out)
	if token == "" {
		t.Fatalf("initial sync returned no sync-token\n%s", out)
	}

	// A new contact arrives after the token was issued.
	doFull(t, ts, "PUT", contactURL("bob.vcf"), bobVCard, nil)

	next := `<d:sync-collection xmlns:d="DAV:"><d:sync-token>` + token + `</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	resp, out = doFull(t, ts, "REPORT", contactURL(""), next, nil)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("incremental sync status %d, want 207\n%s", resp.StatusCode, out)
	}
	if !strings.Contains(out, "Bob Bits") {
		t.Errorf("incremental sync missing the new member\n%s", out)
	}
	if strings.Contains(out, "Ada Lovelace") {
		t.Errorf("incremental sync re-sent an unchanged member\n%s", out)
	}
	if newToken := syncTokenRE.FindString(out); newToken == "" || newToken == token {
		t.Errorf("incremental sync token did not advance: %q -> %q", token, newToken)
	}
}

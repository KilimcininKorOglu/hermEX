package ews

import (
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// fakeDLDir is a static directory that also expands distribution lists, so the
// ExpandDL handler's capability assertion succeeds without a database.
type fakeDLDir struct {
	directory.StaticAccounts
	lists map[string][]string
}

func (f fakeDLDir) ExpandMList(listAddr, _ string) ([]string, directory.MListResult, error) {
	if members, ok := f.lists[strings.ToLower(strings.TrimSpace(listAddr))]; ok {
		return members, directory.MListOK, nil
	}
	return nil, directory.MListNone, nil
}

func dlServer(t *testing.T, lists map[string][]string) *httptest.Server {
	t.Helper()
	d := fakeDLDir{
		StaticAccounts: directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}},
		lists:          lists,
	}
	ts := httptest.NewServer(NewServer(d, d, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

func expandDLBody(address string) string {
	return `<ExpandDL xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<t:Mailbox><t:EmailAddress>` + address + `</t:EmailAddress></t:Mailbox>` +
		`</ExpandDL>`
}

// parsedExpandDL reads the response code and the expanded member addresses.
type parsedExpandDL struct {
	Code    string `xml:"Body>ExpandDLResponse>ResponseMessages>ExpandDLResponseMessage>ResponseCode"`
	Members []struct {
		Email string `xml:"EmailAddress"`
		Type  string `xml:"MailboxType"`
	} `xml:"Body>ExpandDLResponse>ResponseMessages>ExpandDLResponseMessage>DLExpansion>Mailbox"`
}

func expandDL(t *testing.T, ts *httptest.Server, address string) parsedExpandDL {
	t.Helper()
	_, body := soapPost(t, ts, wrapRequest(expandDLBody(address)), true)
	var p parsedExpandDL
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse ExpandDL response: %v\n%s", err, body)
	}
	return p
}

// TestExpandDLMembers proves a distribution list expands to its direct members.
func TestExpandDLMembers(t *testing.T) {
	ts := dlServer(t, map[string][]string{
		"team@hermex.test": {"alice@hermex.test", "bob@hermex.test"},
	})

	p := expandDL(t, ts, "team@hermex.test")
	if p.Code != "NoError" {
		t.Fatalf("ResponseCode = %q, want NoError", p.Code)
	}
	got := map[string]bool{}
	for _, m := range p.Members {
		got[m.Email] = true
		if m.Type != "Mailbox" {
			t.Errorf("MailboxType = %q, want Mailbox", m.Type)
		}
	}
	if !got["alice@hermex.test"] || !got["bob@hermex.test"] {
		t.Errorf("members = %v, want alice and bob", got)
	}
}

// TestExpandDLNotAList proves a non-list address resolves to no results, not an
// empty success or a crash.
func TestExpandDLNotAList(t *testing.T) {
	ts := dlServer(t, map[string][]string{"team@hermex.test": {"alice@hermex.test"}})

	p := expandDL(t, ts, "notalist@hermex.test")
	if p.Code != "ErrorNameResolutionNoResults" {
		t.Errorf("ResponseCode = %q, want ErrorNameResolutionNoResults", p.Code)
	}
	if len(p.Members) != 0 {
		t.Errorf("got %d members for a non-list, want 0", len(p.Members))
	}
}

// TestExpandDLNoCapability proves a directory that cannot expand lists degrades to
// a no-results response rather than faulting.
func TestExpandDLNoCapability(t *testing.T) {
	ts := newTestServer(t) // StaticAccounts: no ExpandMList capability

	p := expandDL(t, ts, "team@hermex.test")
	if p.Code != "ErrorNameResolutionNoResults" {
		t.Errorf("ResponseCode = %q, want ErrorNameResolutionNoResults", p.Code)
	}
}

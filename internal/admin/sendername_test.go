package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestUIDomainSenderNameSave proves the domain-detail outgoing-name form carries the
// internal and external templates through to the directory and acknowledges the save.
func TestUIDomainSenderNameSave(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		domainDetail: directory.DomainDetail{ID: 1, Name: "acme.test"},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	resp := htmxPUT(t, ts, "/admin/ui/domains/1/sendername", session, csrf,
		url.Values{
			"sender_name_internal": {"{name} ({title})"},
			"sender_name_external": {"{name} ({company} - {title})"},
		})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui domain sendername save status %d, want 200", resp.StatusCode)
	}
	if d.senderInt != "{name} ({title})" || d.senderExt != "{name} ({company} - {title})" {
		t.Errorf("save captured (%q, %q), want the internal and external templates", d.senderInt, d.senderExt)
	}
	if !strings.Contains(string(body), `class="ok"`) {
		t.Errorf("save response = %s, want a success acknowledgement", body)
	}
}

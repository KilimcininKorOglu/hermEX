package admin

import (
	"io"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// TestTLSCertUploadNameMustBeCovered stores certificates under SNI names. The
// listener serves a certificate stored under a name to every client asking for
// that name, so a certificate that does not cover it fails every such handshake
// with a name mismatch. The upload must refuse that pairing before it is stored,
// and accept a name the certificate covers, a wildcard one label deep included.
func TestTLSCertUploadNameMustBeCovered(t *testing.T) {
	d := systemAdminDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)
	year := time.Now().Add(365 * 24 * time.Hour)
	single, singleKey := genCertKeyPEM(t, "mail.example.com", []string{"mail.example.com"}, year)
	wild, wildKey := genCertKeyPEM(t, "*.example.com", []string{"*.example.com"}, year)

	cases := []struct {
		name, cert, key string
		stored          bool
	}{
		{"other.example.com", single, singleKey, false},
		{"*.example.com", single, singleKey, false},
		{"mail.example.com", single, singleKey, true},
		{"MAIL.example.com", single, singleKey, true},
		{"*.example.com", wild, wildKey, true},
		{"webmail.example.com", wild, wildKey, true},
		{"a.b.example.com", wild, wildKey, false},
	}
	for _, c := range cases {
		before := len(d.tlsCerts)
		resp := htmxPOST(t, ts, "/admin/ui/tls/upload", session, csrf,
			url.Values{"name": {c.name}, "cert": {c.cert}, "key": {c.key}})
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		stored := slices.ContainsFunc(d.tlsCerts, func(ci directory.TLSCertInfo) bool {
			return ci.Name == strings.ToLower(c.name)
		})
		switch {
		case c.stored && !stored:
			t.Errorf("name %q was refused for a certificate that covers it: %s", c.name, body)
		case !c.stored && (stored || len(d.tlsCerts) != before):
			t.Errorf("name %q was stored for a certificate that does not cover it", c.name)
		case !c.stored && !strings.Contains(string(body), "Upload rejected"):
			t.Errorf("refusing name %q did not tell the operator why: %s", c.name, body)
		}
	}
}

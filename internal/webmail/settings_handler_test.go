package webmail

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/objectstore"
)

// TestSettingsPageManagesSignatures drives the /settings page end to end: adding
// a signature (server-assigned id, stored as HTML), listing it, saving the
// compose format and a default assignment, and deleting the signature (which
// clears the dangling default reference).
func TestSettingsPageManagesSignatures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice")
	st, _ := objectstore.Open(path)
	st.Close()
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	post := func(v url.Values) {
		t.Helper()
		resp, err := c.PostForm(ts.URL+"/settings", v)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	reload := func() webmailSettings {
		t.Helper()
		s2, err := objectstore.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s2.Close()
		cfg, err := loadSettings(s2)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	post(url.Values{"action": {"addsig"}, "signame": {"Work"}, "sigbodyhtml": {"<p>Best, Ali</p>"}})
	cfg := reload()
	if len(cfg.Signatures) != 1 || cfg.Signatures[0].Name != "Work" || !cfg.Signatures[0].IsHTML ||
		!strings.Contains(cfg.Signatures[0].HTML, "Best, Ali") {
		t.Fatalf("signature not stored as expected: %+v", cfg.Signatures)
	}
	sigID := cfg.Signatures[0].ID

	if _, body := get(t, c, ts.URL+"/settings"); !strings.Contains(body, "Work") {
		t.Errorf("settings page does not list the signature")
	}

	post(url.Values{"action": {"save"}, "composeformat": {"plain"}, "defaultnew": {sigID}})
	cfg = reload()
	if cfg.ComposeFormat != "plain" || cfg.DefaultSignatureNew != sigID {
		t.Errorf("preferences not saved: format=%q new=%q", cfg.ComposeFormat, cfg.DefaultSignatureNew)
	}

	post(url.Values{"action": {"delsig"}, "sigid": {sigID}})
	cfg = reload()
	if len(cfg.Signatures) != 0 {
		t.Errorf("signature not deleted: %+v", cfg.Signatures)
	}
	if cfg.DefaultSignatureNew != "" {
		t.Errorf("dangling default reference not cleared: %q", cfg.DefaultSignatureNew)
	}
}

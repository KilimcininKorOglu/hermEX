package objectstore

import (
	"path/filepath"
	"testing"
)

// TestWebmailSettingsRoundTrip verifies the settings primitive: settings are
// absent until written, round-trip a JSON blob (including non-ASCII and markup)
// through the store-root property, are replaced by a later write, and persist
// across a reopen because they live in the object store, not in memory.
func TestWebmailSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mbox")
	s, err := Open(path)
	mustNoErr(t, "open store", err)

	wantEq(t, "initial settings", mustWebmailSettings(t, s), "")

	want := `{"signatures":[{"id":1,"name":"Varsayılan","content":"Saygılarımla,<br><b>Ali</b>","isHTML":true}]}`
	mustNoErr(t, "set webmail settings", s.SetWebmailSettings(want))
	wantEq(t, "settings round-trip", mustWebmailSettings(t, s), want)

	// A later write replaces the previous value.
	mustNoErr(t, "overwrite webmail settings", s.SetWebmailSettings(`{"compose":"html"}`))
	wantEq(t, "settings after overwrite", mustWebmailSettings(t, s), `{"compose":"html"}`)
	s.Close()

	s2, err := Open(path)
	mustNoErr(t, "reopen store", err)
	defer s2.Close()
	wantEq(t, "settings after reopen (the last write survives)", mustWebmailSettings(t, s2), `{"compose":"html"}`)
}

// mustWebmailSettings reads the per-mailbox webmail settings blob.
func mustWebmailSettings(t *testing.T, s *Store) string {
	t.Helper()
	got, err := s.GetWebmailSettings()
	mustNoErr(t, "get webmail settings", err)
	return got
}

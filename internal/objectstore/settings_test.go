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
	if err != nil {
		t.Fatal(err)
	}

	if got, err := s.GetWebmailSettings(); err != nil || got != "" {
		t.Fatalf("initial settings = %q, %v; want empty string and no error", got, err)
	}

	want := `{"signatures":[{"id":1,"name":"Varsayılan","content":"Saygılarımla,<br><b>Ali</b>","isHTML":true}]}`
	if err := s.SetWebmailSettings(want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetWebmailSettings(); err != nil || got != want {
		t.Fatalf("settings round-trip = %q, %v; want %q", got, err, want)
	}

	// A later write replaces the previous value.
	if err := s.SetWebmailSettings(`{"compose":"html"}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetWebmailSettings(); got != `{"compose":"html"}` {
		t.Errorf("after overwrite = %q, want {\"compose\":\"html\"}", got)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got, _ := s2.GetWebmailSettings(); got != `{"compose":"html"}` {
		t.Errorf("settings after reopen = %q, want the last write to survive", got)
	}
}

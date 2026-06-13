package webmail

import (
	"path/filepath"
	"testing"

	"hermex/internal/objectstore"
)

// openWebmailStore opens a fresh mailbox store for settings tests.
func openWebmailStore(t *testing.T) *objectstore.Store {
	t.Helper()
	st, err := objectstore.Open(filepath.Join(t.TempDir(), "alice"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSettingsDefaultsWhenAbsent(t *testing.T) {
	st := openWebmailStore(t)
	s, err := loadSettings(st)
	if err != nil {
		t.Fatal(err)
	}
	if s.ComposeFormat != "html" {
		t.Errorf("default ComposeFormat = %q, want html", s.ComposeFormat)
	}
	if len(s.Signatures) != 0 {
		t.Errorf("default Signatures = %v, want none", s.Signatures)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	st := openWebmailStore(t)
	want := webmailSettings{
		ComposeFormat:       "plain",
		Signatures:          []signature{{ID: "a1", Name: "Varsayılan", HTML: "<b>Ali</b>", IsHTML: true}},
		DefaultSignatureNew: "a1",
	}
	if err := saveSettings(st, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(st)
	if err != nil {
		t.Fatal(err)
	}
	if got.ComposeFormat != "plain" {
		t.Errorf("ComposeFormat = %q, want plain", got.ComposeFormat)
	}
	if got.DefaultSignatureNew != "a1" {
		t.Errorf("DefaultSignatureNew = %q, want a1", got.DefaultSignatureNew)
	}
	if len(got.Signatures) != 1 || got.Signatures[0].Name != "Varsayılan" || got.Signatures[0].HTML != "<b>Ali</b>" || !got.Signatures[0].IsHTML {
		t.Errorf("Signatures = %+v, want one HTML signature 'Varsayılan'", got.Signatures)
	}
	// saveSettings stamps the schema version even when the caller left it zero.
	if got.SchemaVersion != settingsSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, settingsSchemaVersion)
	}
}

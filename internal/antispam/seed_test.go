package antispam

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedModel proves the cold-start floor learned its seed corpus: seed-like
// spam text leans spam and seed-like ham text leans ham.
func TestEmbeddedModel(t *testing.T) {
	m := EmbeddedModel()
	if m == nil {
		t.Fatal("EmbeddedModel returned nil")
	}
	if p := m.Score("you won the lottery claim your prize money now"); p <= 0.5 {
		t.Errorf("seed-spam-like text scored %.3f, want > 0.5", p)
	}
	if p := m.Score("here is the weekly project status report and meeting agenda"); p >= 0.5 {
		t.Errorf("seed-ham-like text scored %.3f, want < 0.5", p)
	}
}

// TestLoadModelPrefersDataDir proves a data_dir model supersedes the embedded
// floor, and a missing one falls back to the floor without error.
func TestLoadModelPrefersDataDir(t *testing.T) {
	dir := t.TempDir()

	m, err := LoadModel(dir)
	if err != nil || m != EmbeddedModel() {
		t.Fatalf("missing model: got (%p, %v), want the embedded floor", m, err)
	}

	custom := NewBayesModel()
	custom.Train("unique marker token", true)
	custom.Train("other plain token", false)
	f, err := os.Create(filepath.Join(dir, ModelFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := custom.Save(f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := LoadModel(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got == EmbeddedModel() {
		t.Error("data_dir model did not supersede the embedded floor")
	}
	if got.SpamMsgs != 1 {
		t.Errorf("loaded model SpamMsgs = %d, want 1", got.SpamMsgs)
	}
}

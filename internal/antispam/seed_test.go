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
	mustNoErr(t, f.Close(), "close custom model file")

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

// TestLoadModelFallsBackOnCorruptFile proves a corrupt model file in data_dir
// returns an error and falls back to EmbeddedModel without altering the file
// on disk or mutating the embedded floor.
func TestLoadModelFallsBackOnCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ModelFileName)
	corrupt := `{"spam_msgs":-5,"ham_msgs":1}`
	mustNoErr(t, os.WriteFile(path, []byte(corrupt), 0o600), "write corrupt model")

	beforeSpam := EmbeddedModel().SpamMsgs
	got, err := LoadModel(dir)
	if err == nil {
		t.Fatal("LoadModel on corrupt file returned nil error, want error")
	}
	if got != EmbeddedModel() {
		t.Error("LoadModel on corrupt file did not fall back to EmbeddedModel")
	}
	wantEq(t, EmbeddedModel().SpamMsgs, beforeSpam, "EmbeddedModel SpamMsgs unchanged")
	onDisk, readErr := os.ReadFile(path)
	mustNoErr(t, readErr, "read corrupt file back")
	wantEq(t, string(onDisk), corrupt, "corrupt file on disk left untouched")
}

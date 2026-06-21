package antispam

import (
	"bytes"
	"testing"
)

// TestBayesClassifies proves the model learns: after training on distinct spam
// and ham vocabularies, held-out spam-like text scores spam and ham-like text
// scores ham. This fails if the prior, smoothing, or log-space math is wrong —
// not merely if Train/Score execute.
func TestBayesClassifies(t *testing.T) {
	m := NewBayesModel()
	for i := 0; i < 5; i++ {
		m.Train("cheap viagra pills buy now discount pharmacy offer", true)
		m.Train("meeting notes project schedule please review attached report", false)
	}
	if p := m.Score("buy cheap discount pills now"); p <= 0.5 {
		t.Errorf("spam-like text scored %.3f, want > 0.5", p)
	}
	if p := m.Score("project meeting schedule review report"); p >= 0.5 {
		t.Errorf("ham-like text scored %.3f, want < 0.5", p)
	}
}

// TestBayesUntrainedNeutral proves a model lacking a baseline in either class
// gives no signal (0.5), so an unbootstrapped filter never condemns mail.
func TestBayesUntrainedNeutral(t *testing.T) {
	if p := NewBayesModel().Score("anything at all"); p != 0.5 {
		t.Errorf("untrained score = %.3f, want 0.5", p)
	}
	m := NewBayesModel()
	m.Train("only spam seen here", true)
	if p := m.Score("spam"); p != 0.5 {
		t.Errorf("spam-only (no ham baseline) score = %.3f, want 0.5", p)
	}
}

// TestBayesRoundTrip proves the JSON persistence preserves counts and scores.
func TestBayesRoundTrip(t *testing.T) {
	m := NewBayesModel()
	m.Train("cheap pills buy now", true)
	m.Train("project meeting review", false)

	var buf bytes.Buffer
	if err := m.Save(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBayesModel(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.SpamMsgs != 1 || got.HamMsgs != 1 || got.SpamTokens["cheap"] != 1 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if m.Score("cheap pills") != got.Score("cheap pills") {
		t.Error("score differs after JSON round trip")
	}
}

// TestTokenize proves the tokenizer lowercases, splits on non-alphanumerics, and
// drops too-short and too-long noise.
func TestTokenize(t *testing.T) {
	got := tokenize("Hi, BUY now! a " + "x" + " " + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	want := map[string]bool{"buy": true, "now": true}
	if len(got) != 2 {
		t.Fatalf("tokens = %v, want exactly [buy now]", got)
	}
	for _, tok := range got {
		if !want[tok] {
			t.Errorf("unexpected token %q", tok)
		}
	}
}

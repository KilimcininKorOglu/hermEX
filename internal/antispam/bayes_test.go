package antispam

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestTrainFromDir proves the corpus trainer reads every file, labels it, and
// reduces it via MessageText so the trained vocabulary matches live scoring.
func TestTrainFromDir(t *testing.T) {
	dir := t.TempDir()
	spam := filepath.Join(dir, "spam")
	ham := filepath.Join(dir, "ham")
	if err := os.Mkdir(spam, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ham, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spam, "1.eml"), []byte("Subject: cheap pills\r\n\r\nbuy now discount viagra"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ham, "1.eml"), []byte("Subject: meeting\r\n\r\nproject schedule review"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewBayesModel()
	if n, err := TrainFromDir(m, spam, true); err != nil || n != 1 {
		t.Fatalf("spam train = (%d, %v), want (1, nil)", n, err)
	}
	if n, err := TrainFromDir(m, ham, false); err != nil || n != 1 {
		t.Fatalf("ham train = (%d, %v), want (1, nil)", n, err)
	}
	if m.SpamMsgs != 1 || m.HamMsgs != 1 {
		t.Fatalf("message counts = spam %d ham %d, want 1/1", m.SpamMsgs, m.HamMsgs)
	}
	// MessageText pulled the subject and body, so spam vocabulary is present.
	if m.SpamTokens["cheap"] != 1 || m.SpamTokens["viagra"] != 1 {
		t.Errorf("spam tokens missing subject/body words: %v", m.SpamTokens)
	}
}

// TestScoreBayesConfidentSpam proves a trained model wired into the Scorer adds
// the Bayes weight and records the probability when content is confidently spam.
func TestScoreBayesConfidentSpam(t *testing.T) {
	m := NewBayesModel()
	for range 8 {
		m.Train("cheap pills viagra discount pharmacy buy now offer cialis", true)
		m.Train("project meeting schedule notes review report attached agenda", false)
	}
	s := &Scorer{
		Weights: DefaultWeights, Threshold: DefaultThreshold,
		Model:       m,
		extractText: func(raw []byte) string { return string(raw) },
	}
	v := s.Score(Input{Raw: []byte("cheap pills discount buy now viagra cialis offer")})
	if v.BayesProb < bayesSpamProb {
		t.Fatalf("BayesProb = %.3f, want >= %.2f", v.BayesProb, bayesSpamProb)
	}
	if v.Score != DefaultWeights.BayesSpam {
		t.Errorf("score = %d, want %d (Bayes only)", v.Score, DefaultWeights.BayesSpam)
	}
}

// TestScoreBayesDormantWithoutModel proves content scoring contributes nothing
// when no model is set, even with text extraction wired.
func TestScoreBayesDormantWithoutModel(t *testing.T) {
	s := &Scorer{
		Weights: DefaultWeights, Threshold: DefaultThreshold,
		extractText: func([]byte) string { return "cheap pills buy now" },
	}
	v := s.Score(Input{Raw: []byte("cheap pills buy now")})
	if v.BayesProb != 0 || v.Score != 0 {
		t.Errorf("verdict = %+v, want no Bayes contribution", v)
	}
}

// TestLoadModelFileMissing proves a missing model file is dormant, not an error.
func TestLoadModelFileMissing(t *testing.T) {
	m, err := LoadModelFile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || m != nil {
		t.Errorf("missing model = (%v, %v), want (nil, nil)", m, err)
	}
}

// TestBayesClassifies proves the model learns: after training on distinct spam
// and ham vocabularies, held-out spam-like text scores spam and ham-like text
// scores ham. This fails if the prior, smoothing, or log-space math is wrong —
// not merely if Train/Score execute.
func TestBayesClassifies(t *testing.T) {
	m := NewBayesModel()
	for range 5 {
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

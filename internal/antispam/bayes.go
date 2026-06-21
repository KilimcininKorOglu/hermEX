package antispam

import (
	"encoding/json"
	"io"
	"math"
	"strings"
	"unicode"
)

// BayesModel is a naive Bayes spam model: per-token occurrence counts in each
// class and the number of messages trained per class. It is persisted as JSON so
// the bootstrap model can be embedded and reviewed in a diff, and incremental
// self-training can merge into it.
type BayesModel struct {
	SpamTokens map[string]int `json:"spam_tokens"`
	HamTokens  map[string]int `json:"ham_tokens"`
	SpamMsgs   int            `json:"spam_msgs"`
	HamMsgs    int            `json:"ham_msgs"`
}

// NewBayesModel returns an empty, trainable model.
func NewBayesModel() *BayesModel {
	return &BayesModel{SpamTokens: map[string]int{}, HamTokens: map[string]int{}}
}

// Train updates the model with one message's text and its known class.
func (m *BayesModel) Train(text string, spam bool) {
	toks := tokenize(text)
	if spam {
		m.SpamMsgs++
		for _, t := range toks {
			m.SpamTokens[t]++
		}
	} else {
		m.HamMsgs++
		for _, t := range toks {
			m.HamTokens[t]++
		}
	}
}

// Score returns the probability (0..1) that text is spam, by multinomial naive
// Bayes with Laplace smoothing computed in log space. A model not trained on both
// classes has no baseline and returns 0.5 — no signal.
func (m *BayesModel) Score(text string) float64 {
	if m.SpamMsgs == 0 || m.HamMsgs == 0 {
		return 0.5
	}
	total := float64(m.SpamMsgs + m.HamMsgs)
	logSpam := math.Log(float64(m.SpamMsgs) / total)
	logHam := math.Log(float64(m.HamMsgs) / total)

	spamTotal := float64(tokenSum(m.SpamTokens))
	hamTotal := float64(tokenSum(m.HamTokens))
	vocab := float64(vocabSize(m.SpamTokens, m.HamTokens))
	for _, t := range tokenize(text) {
		logSpam += math.Log((float64(m.SpamTokens[t]) + 1) / (spamTotal + vocab))
		logHam += math.Log((float64(m.HamTokens[t]) + 1) / (hamTotal + vocab))
	}
	// The logistic of the log-odds keeps the result bounded and underflow-free:
	// only the difference is exponentiated, never the raw log-probabilities.
	return 1 / (1 + math.Exp(logHam-logSpam))
}

// Save writes the model as indented JSON.
func (m *BayesModel) Save(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// LoadBayesModel reads a JSON model, leaving its maps non-nil so it is trainable.
func LoadBayesModel(r io.Reader) (*BayesModel, error) {
	m := NewBayesModel()
	if err := json.NewDecoder(r).Decode(m); err != nil {
		return nil, err
	}
	if m.SpamTokens == nil {
		m.SpamTokens = map[string]int{}
	}
	if m.HamTokens == nil {
		m.HamTokens = map[string]int{}
	}
	return m, nil
}

// tokenize splits text into lowercased word tokens for the model: maximal runs of
// letters and digits, length-filtered to drop single characters and long noise
// (base64 blobs, hashes).
func tokenize(text string) []string {
	var toks []string
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if n := len(f); n >= 3 && n <= 40 {
			toks = append(toks, f)
		}
	}
	return toks
}

// tokenSum totals a token-count map.
func tokenSum(m map[string]int) int {
	sum := 0
	for _, c := range m {
		sum += c
	}
	return sum
}

// vocabSize counts the distinct tokens across both classes (the Laplace vocabulary).
func vocabSize(a, b map[string]int) int {
	seen := make(map[string]struct{}, len(a)+len(b))
	for t := range a {
		seen[t] = struct{}{}
	}
	for t := range b {
		seen[t] = struct{}{}
	}
	return len(seen)
}

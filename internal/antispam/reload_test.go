package antispam

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReloadOnceHotSwapsSettings proves an edited settings version is applied to
// the live Scorer on the tick: raising the threshold flips a message from spam to
// ham, without a restart.
func TestReloadOnceHotSwapsSettings(t *testing.T) {
	s := &Scorer{checkSPF: func(net.IP, string, string) AuthResult { return AuthFail }}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: 1})
	r := NewReloader(s, t.TempDir(), nil)

	cfg := &Config{Weights: DefaultWeights, Threshold: 1}
	ver := int64(100)
	r.WatchSettings(func() (*Config, int64, bool) { return cfg, ver, true })

	if names := r.reloadOnce(); len(names) != 0 {
		t.Fatalf("reloadOnce with no settings change = %v, want none", names)
	}
	in := Input{Raw: []byte("x"), ClientIP: net.IPv4(1, 2, 3, 4), MailFrom: "a@x"}
	if v := s.Score(in); !v.Spam {
		t.Fatal("threshold 1 + SPF fail should be spam before the edit")
	}

	// An admin edit raises the threshold and bumps the version.
	cfg = &Config{Weights: DefaultWeights, Threshold: 100}
	ver = 200
	if names := r.reloadOnce(); len(names) != 1 || names[0] != "settings" {
		t.Fatalf("reloadOnce after edit = %v, want [settings]", names)
	}
	if v := s.Score(in); v.Spam {
		t.Error("after the settings reload the same message should be ham")
	}
}

// TestReloadOnceHotSwapsAccess proves an edited allow/block list is applied to the
// live Scorer when its content hash changes, and not re-applied while unchanged.
func TestReloadOnceHotSwapsAccess(t *testing.T) {
	s := &Scorer{}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: 100})
	r := NewReloader(s, t.TempDir(), nil)

	list := NewAccessList(map[string]string{"a@x.example": AccessBlock})
	hash := uint64(1)
	r.WatchAccess(func() (*AccessList, uint64, bool) { return list, hash, true })

	if names := r.reloadOnce(); len(names) != 0 {
		t.Fatalf("reloadOnce with no access change = %v, want none", names)
	}

	// An admin adds a rule: a new list with a different content hash.
	list = NewAccessList(map[string]string{"a@x.example": AccessBlock, "b@y.example": AccessBlock})
	hash = 2
	if names := r.reloadOnce(); len(names) != 1 || names[0] != "access rules" {
		t.Fatalf("reloadOnce after edit = %v, want [access rules]", names)
	}
	if v := s.Score(Input{Raw: []byte("x"), MailFrom: "b@y.example"}); !v.Spam {
		t.Error("the hot-swapped access list should block the newly added sender")
	}
}

// setMod stamps a file's modification time so the reloader's mtime comparison is
// deterministic (filesystem timestamps are otherwise coarse).
func setMod(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// TestReloadOnceHotSwapsRuleset proves a refreshed data_dir ruleset is swapped
// into the live Scorer without a restart: after reload the new rule scores and the
// old one no longer does.
func TestReloadOnceHotSwapsRuleset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, RulesFileName)
	old := time.Now().Add(-time.Hour)

	if err := os.WriteFile(path, []byte("body OLD /oldspam/\nscore OLD 9.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setMod(t, path, old)

	s := &Scorer{}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: DefaultThreshold})
	rs, _ := LoadRulesFile(path)
	s.SetRules(rs)
	r := NewReloader(s, dir, nil)

	if names := r.reloadOnce(); len(names) != 0 {
		t.Fatalf("reloadOnce with no change = %v, want none", names)
	}

	// Operator refreshes the ruleset (newer mtime).
	if err := os.WriteFile(path, []byte("body NEW /freshspam/\nscore NEW 9.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setMod(t, path, time.Now())

	if names := r.reloadOnce(); len(names) != 1 || names[0] != "ruleset" {
		t.Fatalf("reloadOnce after refresh = %v, want [ruleset]", names)
	}
	if v := s.Score(Input{Raw: []byte("Subject: x\r\n\r\nthis is freshspam now")}); v.SAScore != 9.0 {
		t.Errorf("new rule not live: SAScore = %v, want 9.0", v.SAScore)
	}
	if v := s.Score(Input{Raw: []byte("Subject: x\r\n\r\nthis is oldspam now")}); v.SAScore != 0 {
		t.Errorf("old rule still live after swap: SAScore = %v, want 0", v.SAScore)
	}
}

// TestScoreSafeUnderConcurrentReload exercises Score concurrently with the
// SetConfig/SetRules/SetModel hot-swaps the reloader performs. It is the teeth
// behind the "hot-swap without a restart" claim: run under -race, a plain pointer
// field would report a data race here; the atomic pointers must not.
func TestScoreSafeUnderConcurrentReload(t *testing.T) {
	s := &Scorer{extractText: func(b []byte) string { return string(b) }}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: DefaultThreshold})
	s.SetRules(ParseSARules("body X /spammy/\nscore X 1.0\n"))
	raw := []byte("Subject: x\r\n\r\nsome spammy text here")

	done := make(chan struct{})
	go func() {
		for i := range 500 {
			s.SetConfig(&Config{Weights: DefaultWeights, Threshold: DefaultThreshold + i%3})
			s.SetRules(ParseSARules("body Y /other/\nscore Y 2.0\n"))
			s.SetModel(NewBayesModel())
			s.SetAccess(NewAccessList(map[string]string{"x@evil.example": AccessBlock}))
		}
		close(done)
	}()
	for range 500 {
		s.Score(Input{Raw: raw})
	}
	<-done
}

// TestReloadOnceHotSwapsModel proves a retrained model written to data_dir is
// swapped into the live Scorer without a restart.
func TestReloadOnceHotSwapsModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ModelFileName)
	raw := []byte("zzzspam zzzspam zzzspam")

	// Initial model: untrained on the phrase -> no confident spam signal.
	empty := NewBayesModel()
	if err := empty.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	setMod(t, path, time.Now().Add(-time.Hour))

	s := &Scorer{extractText: func(b []byte) string { return string(b) }}
	s.SetConfig(&Config{Weights: DefaultWeights, Threshold: DefaultThreshold})
	m0, _ := LoadModelFile(path)
	s.SetModel(m0)
	r := NewReloader(s, dir, nil)

	if v := s.Score(Input{Raw: raw}); v.BayesProb >= bayesSpamProb {
		t.Fatalf("untrained model already confident: BayesProb = %v", v.BayesProb)
	}

	// A retrain writes a model confident the phrase is spam.
	trained := NewBayesModel()
	for range 20 {
		trained.Train("zzzspam", true)
		trained.Train("ham words here", false)
	}
	if err := trained.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	setMod(t, path, time.Now())

	if names := r.reloadOnce(); len(names) != 1 || names[0] != "model" {
		t.Fatalf("reloadOnce after retrain = %v, want [model]", names)
	}
	if v := s.Score(Input{Raw: raw}); v.BayesProb < bayesSpamProb {
		t.Errorf("retrained model not live: BayesProb = %v, want >= %v", v.BayesProb, bayesSpamProb)
	}
}

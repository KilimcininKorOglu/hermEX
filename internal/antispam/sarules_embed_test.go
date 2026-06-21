package antispam

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedRulesParse proves the vendored Apache SpamAssassin baseline parses
// into a usable ruleset: a substantial number of live regex rules, and the drop
// counters are non-zero, proving the parser actually filtered the real ruleset
// (network/plugin/uncompilable rules were excluded, not silently accepted).
func TestEmbeddedRulesParse(t *testing.T) {
	rs := EmbeddedRules()
	rules, metas := rs.RuleCount()
	if rules < 100 {
		t.Errorf("live rules = %d, want a substantial baseline (>=100)", rules)
	}
	if rs.SkippedRules == 0 {
		t.Errorf("SkippedRules = 0, but the real ruleset has network/plugin/uncompilable rules to drop")
	}
	t.Logf("embedded baseline: %d rules, %d metas, %d skipped rules, %d dropped metas",
		rules, metas, rs.SkippedRules, rs.DroppedMetas)
}

// TestLoadRulesSeedsDataDir proves first run seeds the live ruleset into data_dir
// (with its provenance header) and returns a usable ruleset.
func TestLoadRulesSeedsDataDir(t *testing.T) {
	dir := t.TempDir()
	rs, err := LoadRules(dir)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if rules, _ := rs.RuleCount(); rules < 100 {
		t.Errorf("seeded ruleset has %d rules, want the baseline", rules)
	}
	data, err := os.ReadFile(filepath.Join(dir, RulesFileName))
	if err != nil {
		t.Fatalf("seeded file not written: %v", err)
	}
	if !strings.Contains(string(data), "Apache SpamAssassin") {
		t.Errorf("seeded file missing its provenance header")
	}
}

// TestLoadRulesUsesExistingFileWithoutReseeding proves an existing data_dir
// ruleset is loaded as-is and never overwritten by the embedded baseline.
func TestLoadRulesUsesExistingFileWithoutReseeding(t *testing.T) {
	dir := t.TempDir()
	custom := "body OPERATOR_RULE /operator chose this/\nscore OPERATOR_RULE 1.0\n"
	path := filepath.Join(dir, RulesFileName)
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	rs, err := LoadRules(dir)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	// Only the operator's single rule loaded — not the hundreds in the baseline.
	if rules, _ := rs.RuleCount(); rules != 1 {
		t.Errorf("live rules = %d, want 1 (the operator's file, not the baseline)", rules)
	}
	// The file was not re-seeded over.
	data, _ := os.ReadFile(path)
	if string(data) != custom {
		t.Errorf("existing ruleset was overwritten:\n%s", data)
	}
}

func TestLoadRulesFileMissing(t *testing.T) {
	rs, err := LoadRulesFile(filepath.Join(t.TempDir(), "nope.cf"))
	if rs != nil || err != nil {
		t.Errorf("LoadRulesFile(missing) = (%v, %v), want (nil, nil)", rs, err)
	}
}

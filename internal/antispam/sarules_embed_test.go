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
		t.Errorf("seeded ruleset has %d rules, want the baseline (>=100)", rules)
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
	// Only the operator's single rule loaded, not the hundreds in the baseline.
	if rules, metas := rs.RuleCount(); rules != 1 || metas != 0 {
		t.Errorf("live rules/metas = %d/%d, want 1/0 (the operator's file, not the baseline)", rules, metas)
	}
	// The file was not re-seeded over.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the operator's ruleset back: %v", err)
	}
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

// TestConcatRulesDir proves the refresh helper concatenates a directory's .cf
// files (sorted) into a parseable ruleset, and errors on a directory with none.
func TestConcatRulesDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "20_a.cf"), []byte("body A /alpha/\nscore A 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20_b.cf"), []byte("body B /bravo/\nscore B 2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-.cf file must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("not a rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := ConcatRulesDir(dir)
	if err != nil {
		t.Fatalf("ConcatRulesDir: %v", err)
	}
	if rules, metas := ParseSARules(string(data)).RuleCount(); rules != 2 || metas != 0 {
		t.Errorf("concatenated ruleset has %d rules / %d metas, want 2/0 (A and B)", rules, metas)
	}

	if _, err := ConcatRulesDir(t.TempDir()); err == nil {
		t.Error("ConcatRulesDir on an empty directory should error")
	}
}

// TestRulesLoadRefusesHostilePaths verifies unreadable paths return errors,
// LoadRules falls back to EmbeddedRules without altering the filesystem, and
// ConcatRulesDir refuses missing or non-.cf directories.
func TestRulesLoadRefusesHostilePaths(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, RulesFileName)
	mustNoErr(t, os.Mkdir(rulesDir, 0o700), "create directory at rules path")

	if rs, err := LoadRulesFile(rulesDir); err == nil || rs != nil {
		t.Errorf("LoadRulesFile(dir) = (%v, %v), want (nil, error)", rs, err)
	}
	rs, err := LoadRules(dir)
	if err == nil {
		t.Error("LoadRules with directory at rules path should return error")
	}
	if rs != EmbeddedRules() {
		t.Error("LoadRules should fall back to EmbeddedRules on read error")
	}
	fi, statErr := os.Stat(rulesDir)
	mustNoErr(t, statErr, "rules directory preserved")
	wantEq(t, fi.IsDir(), true, "rules path still a directory")

	nonCFDir := t.TempDir()
	mustNoErr(t, os.WriteFile(filepath.Join(nonCFDir, "README"), []byte("x"), 0o644), "write non-cf file")
	if got, err := ConcatRulesDir(nonCFDir); err == nil || got != nil {
		t.Errorf("ConcatRulesDir(non-cf only) = (%v, %v), want (nil, error)", got, err)
	}
}

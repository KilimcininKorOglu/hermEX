package antispam

import (
	"slices"
	"testing"
)

// sampleRules exercises every supported rule kind plus the three drop paths
// (plugin eval, RE2-incompatible regex, network flag) and a meta that must be
// dropped because it depends on a dropped rule.
const sampleRules = `
# header rule with a score
header SUBJ_VIAGRA  Subject =~ /viagra/i
score  SUBJ_VIAGRA  2.5

# body rule
body   BODY_FREEMONEY  /\bfree money\b/i
score  BODY_FREEMONEY  1.5

# uri rule
uri    URI_SHORTENER  /bit\.ly/i
score  URI_SHORTENER  1.0

# exists header rule
header HAS_LISTID  exists:List-Id
score  HAS_LISTID  0.5

# negated header rule: fires when the Date header is absent
header NO_DATE  Date !~ /\d/
score  NO_DATE  0.7

# nice rule with a negative score (ham signal)
header FROM_TRUSTED  From =~ /@trusted\.example/i
score  FROM_TRUSTED  -1.5

# plugin/eval rule -> skipped
header SPF_PASS  eval:check_spf()

# RE2-incompatible (backreference) -> skipped
body   DOUBLED  /(\w+)\s+\1/

# network rule -> skipped
header RBL_HIT  X-Whatever =~ /listed/
tflags RBL_HIT  net

# meta over available rules: both must fire
meta   COMBO_SPAM  (SUBJ_VIAGRA && BODY_FREEMONEY)
score  COMBO_SPAM  3.0

# meta depending on a dropped rule -> dropped
meta   BAD_META  (SUBJ_VIAGRA && DOUBLED)
score  BAD_META  9.0
`

func TestParseSARulesDropCounts(t *testing.T) {
	rs := ParseSARules(sampleRules)
	if rs.SkippedRules != 3 {
		t.Errorf("SkippedRules = %d, want 3 (eval, backreference, net)", rs.SkippedRules)
	}
	if rs.DroppedMetas != 1 {
		t.Errorf("DroppedMetas = %d, want 1 (BAD_META depends on the dropped DOUBLED)", rs.DroppedMetas)
	}
	rules, metas := rs.RuleCount()
	if rules != 6 {
		t.Errorf("live rules = %d, want 6", rules)
	}
	if metas != 1 {
		t.Errorf("live metas = %d, want 1 (only COMBO_SPAM survives)", metas)
	}
}

// TestEvaluateSumsHitScores proves the score is the sum of the fired rules and
// metas — here viagra subject + free money body + a bit.ly link + a List-Id all
// fire, and because the two component rules fire COMBO_SPAM fires too.
func TestEvaluateSumsHitScores(t *testing.T) {
	rs := ParseSARules(sampleRules)
	raw := []byte("Subject: cheap Viagra now\r\n" +
		"From: deals@spam.example\r\n" +
		"List-Id: <promo.spam.example>\r\n" +
		"Date: Mon, 1 Jan 2026 00:00:00 +0000\r\n" +
		"\r\n" +
		"Get free money fast at http://bit.ly/x123\r\n")

	score, fired := rs.Evaluate(raw)
	// 2.5 + 1.5 + 1.0 + 0.5 + 3.0 = 8.5
	if score < 8.49 || score > 8.51 {
		t.Errorf("score = %v, want 8.5; fired=%v", score, fired)
	}
	for _, want := range []string{"SUBJ_VIAGRA", "BODY_FREEMONEY", "URI_SHORTENER", "HAS_LISTID", "COMBO_SPAM"} {
		if !slices.Contains(fired, want) {
			t.Errorf("expected %s to fire; fired=%v", want, fired)
		}
	}
	if slices.Contains(fired, "BAD_META") {
		t.Errorf("BAD_META was dropped and must never fire; fired=%v", fired)
	}
}

// TestMetaRequiresAllComponents proves the meta's AND logic: with only the viagra
// subject (no "free money" body) the component rule fires but COMBO_SPAM does not.
func TestMetaRequiresAllComponents(t *testing.T) {
	rs := ParseSARules(sampleRules)
	raw := []byte("Subject: viagra special\r\nDate: x1\r\n\r\nhello there friend\r\n")

	_, fired := rs.Evaluate(raw)
	if !slices.Contains(fired, "SUBJ_VIAGRA") {
		t.Fatalf("SUBJ_VIAGRA should fire; fired=%v", fired)
	}
	if slices.Contains(fired, "COMBO_SPAM") {
		t.Errorf("COMBO_SPAM must not fire without BODY_FREEMONEY; fired=%v", fired)
	}
}

// TestNegatedHeaderFiresWhenAbsent proves a "!~" header rule fires when the header
// is missing, and a nice (negative-score) rule subtracts from the total.
func TestNegatedHeaderFiresWhenAbsent(t *testing.T) {
	rs := ParseSARules(sampleRules)
	// No Date header -> NO_DATE (+0.7) fires; trusted From (-1.5) fires.
	raw := []byte("Subject: hi\r\nFrom: alice@trusted.example\r\n\r\nplain note\r\n")

	score, fired := rs.Evaluate(raw)
	if !slices.Contains(fired, "NO_DATE") {
		t.Errorf("NO_DATE should fire when Date is absent; fired=%v", fired)
	}
	if !slices.Contains(fired, "FROM_TRUSTED") {
		t.Errorf("FROM_TRUSTED should fire; fired=%v", fired)
	}
	// 0.7 - 1.5 = -0.8
	if score < -0.81 || score > -0.79 {
		t.Errorf("score = %v, want -0.8 (nice rule subtracts)", score)
	}
}

func TestEvaluateCleanMessageScoresZero(t *testing.T) {
	rs := ParseSARules(sampleRules)
	raw := []byte("Subject: lunch tomorrow?\r\nFrom: bob@work.example\r\nDate: today1\r\n\r\nSee you at noon.\r\n")

	score, fired := rs.Evaluate(raw)
	if score != 0 || len(fired) != 0 {
		t.Errorf("clean message scored %v with fired=%v, want 0 / none", score, fired)
	}
}

// TestMetaComparisonAndOr exercises comparison and || operators in a meta over
// counts, confirming the expression evaluator beyond plain &&.
func TestMetaComparisonAndOr(t *testing.T) {
	rules := `
body A /alpha/
body B /bravo/
body C /charlie/
meta M  (A + B >= 2) || C
score M 4.0
`
	rs := ParseSARules(rules)
	if _, metas := rs.RuleCount(); metas != 1 {
		t.Fatalf("want 1 meta, got %d", metas)
	}

	// Two of A/B fire -> A+B>=2 true -> M fires.
	if score, _ := rs.Evaluate([]byte("Subject: x\r\n\r\nalpha and bravo\r\n")); score != 4.0 {
		t.Errorf("alpha+bravo: M score = %v, want 4.0", score)
	}
	// Only C fires -> the || branch fires M.
	if score, _ := rs.Evaluate([]byte("Subject: x\r\n\r\njust charlie here\r\n")); score != 4.0 {
		t.Errorf("charlie: M score = %v, want 4.0", score)
	}
	// Only A fires -> A+B==1 and no C -> M must not fire.
	if score, _ := rs.Evaluate([]byte("Subject: x\r\n\r\nalpha only\r\n")); score != 0 {
		t.Errorf("alpha only: M score = %v, want 0", score)
	}
}

func TestMetaSyntaxErrorIsDropped(t *testing.T) {
	rs := ParseSARules("body A /x/\nmeta BROKEN (A && )\nscore BROKEN 1.0\n")
	if rs.DroppedMetas != 1 {
		t.Errorf("DroppedMetas = %d, want 1 for the malformed expression", rs.DroppedMetas)
	}
	if _, metas := rs.RuleCount(); metas != 0 {
		t.Errorf("live metas = %d, want 0", metas)
	}
}

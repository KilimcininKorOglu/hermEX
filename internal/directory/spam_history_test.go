package directory

import (
	"fmt"
	"strings"
	"testing"

	"hermex/internal/logging"
)

func setupSpamHistory(t *testing.T) *SQLDirectory {
	t.Helper()
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)
	res, err := db.Exec("DELETE FROM spam_history")
	mustNoErr(t, "clear the spam history", err)
	cleared, err := res.RowsAffected()
	mustNoErr(t, "count the cleared verdicts", err)
	wantEq(t, "cleared verdict count is not negative", cleared >= 0, true)
	return d
}

// TestSpamHistoryRecordAndList proves recorded verdicts come back newest-first
// with their fields intact, the directory backend for the admin Spam History page.
func TestSpamHistoryRecordAndList(t *testing.T) {
	d := setupSpamHistory(t)

	for i := 1; i <= 3; i++ {
		err := d.RecordSpamVerdict(SpamVerdict{
			Time: int64(i), MailFrom: fmt.Sprintf("s%d@ext.example", i),
			RemoteAddr: "203.0.113.9", Score: i * 4, Spam: i == 3,
			Reasons: "SPF fail; Bayesian: likely spam",
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := d.RecentSpamVerdicts(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("RecentSpamVerdicts = %d rows, want 3", len(got))
	}
	// Newest first: the third record (Spam, score 12) leads.
	if got[0].MailFrom != "s3@ext.example" || !got[0].Spam || got[0].Score != 12 {
		t.Errorf("newest verdict = %+v, want s3 spam score 12", got[0])
	}
	if got[0].Reasons != "SPF fail; Bayesian: likely spam" || got[0].RemoteAddr != "203.0.113.9" {
		t.Errorf("verdict fields not preserved: %+v", got[0])
	}
}

// TestSpamHistoryReasonsTruncated proves an over-long reasons string is truncated
// to the column width rather than failing the insert.
func TestSpamHistoryReasonsTruncated(t *testing.T) {
	d := setupSpamHistory(t)
	if err := d.RecordSpamVerdict(SpamVerdict{Time: 1, MailFrom: "a@x", Reasons: strings.Repeat("x", 600)}); err != nil {
		t.Fatal(err)
	}
	got, err := d.RecentSpamVerdicts(1)
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %v err %v", got, err)
	}
	if len(got[0].Reasons) != 512 {
		t.Errorf("reasons length = %d, want truncated to 512", len(got[0].Reasons))
	}
}

// TestSpamHistoryRetention proves the table is bounded: with the cap lowered, only
// roughly the newest cap rows survive after more inserts than the cap.
func TestSpamHistoryRetention(t *testing.T) {
	d := setupSpamHistory(t)
	d.SetSpamHistoryRetain(3)

	for i := range 6 {
		if err := d.RecordSpamVerdict(SpamVerdict{Time: int64(i), MailFrom: fmt.Sprintf("s%d@x", i)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.RecentSpamVerdicts(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("retention cap not enforced: %d rows survived, want 3", len(got))
	}
	// The prune keeps the newest rows, not the first ones written.
	for i, want := range []string{"s5@x", "s4@x", "s3@x"} {
		wantEq(t, fmt.Sprintf("surviving verdict %d", i), got[i].MailFrom, want)
	}
}

// TestSpamHistoryPruneFailureIsLogged proves a retention-prune failure does
// not fail RecordSpamVerdict and records a directory warning event.
func TestSpamHistoryPruneFailureIsLogged(t *testing.T) {
	d := setupSpamHistory(t)
	d.SetSpamHistoryRetain(1)
	sink := &captureSink{}
	d.SetLogger(logging.New(sink))

	mustNoErr(t, "record first verdict", d.RecordSpamVerdict(SpamVerdict{Time: 1, MailFrom: "s1@x"}))
	installFailTrigger(t, d.db, "test_spam_prune_fail", "DELETE", "spam_history")
	mustNoErr(t, "record second verdict despite prune failure", d.RecordSpamVerdict(SpamVerdict{Time: 2, MailFrom: "s2@x"}))

	got, err := d.RecentSpamVerdicts(10)
	mustNoErr(t, "list verdicts", err)
	wantEq(t, "verdicts kept when prune fails", len(got), 2)
	wantWarnEvent(t, sink, "directory.spam_history_prune.fail")
}

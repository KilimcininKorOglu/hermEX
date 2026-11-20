package objectstore

import (
	"fmt"
	"sync"
	"testing"
)

func claim(t *testing.T, st *Store, list, sender string) bool {
	t.Helper()
	first, err := st.ClaimAutoReply(list, sender)
	if err != nil {
		t.Fatalf("claim %s for %s: %v", sender, list, err)
	}
	return first
}

// TestAConcurrentClaimAnswersOnce: two deliveries from one sender run on their
// own handles at once, and exactly one of them may send the reply.
func TestAConcurrentClaimAnswersOnce(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	const n = 8
	var wg sync.WaitGroup
	results := make(chan bool, n)
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			h, err := Open(dir)
			if err != nil {
				errs <- err
				return
			}
			defer h.Close()
			first, err := h.ClaimAutoReply(AutoReplyListOOF, "alice@hermex.test")
			if err != nil {
				errs <- err
				return
			}
			results <- first
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent claim failed: %v", err)
	}
	firsts := 0
	for r := range results {
		if r {
			firsts++
		}
	}
	if firsts != 1 {
		t.Errorf("%d of %d concurrent claims were first, want exactly 1", firsts, n)
	}
}

// TestAFullHistoryForgetsItsOldestSender: the history is bounded, and the bound
// must never cost a sender their first reply. A full list forgets its oldest
// sender, who is then answered once more.
func TestAFullHistoryForgetsItsOldestSender(t *testing.T) {
	st := openTestStore(t)
	for i := range maxAutoReplySenders {
		claim(t, st, AutoReplyListOOF, fmt.Sprintf("s%d@x.test", i))
	}
	if !claim(t, st, AutoReplyListOOF, "new@x.test") {
		t.Fatal("a new sender was refused a reply because the history is full")
	}
	if !claim(t, st, AutoReplyListOOF, "s0@x.test") {
		t.Error("the oldest sender is still held after the list overflowed")
	}
	if claim(t, st, AutoReplyListOOF, "new@x.test") {
		t.Error("the newest sender was answered twice")
	}
}

// TestOOFSettingsWriteEndsThePeriodOnlyWhenChanged: both settings writers start a
// new period on a change and keep it on an identical rewrite, which EWS and
// ActiveSync clients send. Another list (a vacation rule) is never reset by an
// out-of-office change.
func TestOOFSettingsWriteEndsThePeriodOnlyWhenChanged(t *testing.T) {
	st := openTestStore(t)
	on := OOFSettings{Enabled: true, InternalReply: "Away."}
	if err := st.SetOOFSettings(on); err != nil {
		t.Fatal(err)
	}
	rule := RuleAutoReplyList(7, "On leave.")
	claim(t, st, AutoReplyListOOF, "alice@x.test")
	claim(t, st, rule, "alice@x.test")

	if err := st.SetOOFSettings(on); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateOOFSettings(func(*OOFSettings) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if claim(t, st, AutoReplyListOOF, "alice@x.test") {
		t.Fatal("an identical rewrite of the settings started a new period")
	}

	if err := st.UpdateOOFSettings(func(c *OOFSettings) error { c.InternalReply = "Back Monday."; return nil }); err != nil {
		t.Fatal(err)
	}
	if !claim(t, st, AutoReplyListOOF, "alice@x.test") {
		t.Error("a reworded reply through UpdateOOFSettings kept the old period")
	}
	if claim(t, st, rule, "alice@x.test") {
		t.Error("an out-of-office change reset a vacation rule's history")
	}
}

// TestAnAddressIsOneSenderInAnyCase: the claim normalizes the address itself, so
// a caller that passes it as written cannot answer one sender twice.
func TestAnAddressIsOneSenderInAnyCase(t *testing.T) {
	st := openTestStore(t)
	claim(t, st, AutoReplyListOOF, "alice@x.test")
	if claim(t, st, AutoReplyListOOF, " Alice@X.Test ") {
		t.Error("the same address in another case was answered again")
	}
	if claim(t, st, AutoReplyListOOF, "") {
		t.Error("an empty sender was claimed as a first reply")
	}
}

// TestARewordedRuleIsANewList: a vacation rule whose reply changed answers its
// senders again, and two rules keep separate histories.
func TestARewordedRuleIsANewList(t *testing.T) {
	st := openTestStore(t)
	claim(t, st, RuleAutoReplyList(1, "Away."), "alice@x.test")
	if !claim(t, st, RuleAutoReplyList(1, "Away until May."), "alice@x.test") {
		t.Error("a reworded rule did not answer an earlier sender")
	}
	if !claim(t, st, RuleAutoReplyList(2, "Away."), "alice@x.test") {
		t.Error("a second rule with the same text shares the first rule's history")
	}
}

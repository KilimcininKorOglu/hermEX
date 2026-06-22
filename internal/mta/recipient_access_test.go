package mta

import (
	"path/filepath"
	"testing"

	"hermex/internal/antispam"
	"hermex/internal/directory"
)

// fixedRecipAccess is a per-maildir personal allow/block rule set; a maildir absent
// from the map has no rules.
type fixedRecipAccess map[string]map[string]string

func (f fixedRecipAccess) RecipientRulesForMaildir(maildir string) (map[string]string, error) {
	return f[maildir], nil
}

// The five tests below are the precedence matrix the operator chose: an operator block
// beats a recipient's allow, a recipient's block narrows an operator allow, and a
// recipient's allow rescues a message — but never a hard DMARC failure. Each encodes
// the policy, not just the mechanism.

// TestRecipientAllowCannotRescueOperatorBlock proves an operator block is absolute: a
// recipient's own allow for the same sender cannot pull the message out of Junk, so an
// admin-blocked phishing sender stays blocked for everyone.
func TestRecipientAllowCannotRescueOperatorBlock(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{
		Accounts:        accounts,
		Scorer:          &recordingScorer{verdict: antispam.Verdict{Score: 3, Spam: true, AccessMatched: true, AccessAction: antispam.AccessBlock}},
		RecipientAccess: fixedRecipAccess{mbox: {"evil.example": antispam.AccessAllow}},
	}
	deliverInbound(t, b, "spammer@evil.example", "alice@test")
	if junk, inbox := folderCounts(t, mbox); junk != 1 || inbox != 0 {
		t.Errorf("operator block + user allow: junk=%d inbox=%d, want it in Junk (an operator block is absolute)", junk, inbox)
	}
}

// TestRecipientBlockNarrowsOperatorAllow proves a recipient's own block overrides an
// operator allow: the operator allowed the sender server-wide, but this recipient does
// not want it, so it lands in their Junk.
func TestRecipientBlockNarrowsOperatorAllow(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{
		Accounts:        accounts,
		Scorer:          &recordingScorer{verdict: antispam.Verdict{Score: 3, Spam: false, AccessMatched: true, AccessAction: antispam.AccessAllow}},
		RecipientAccess: fixedRecipAccess{mbox: {"evil.example": antispam.AccessBlock}},
	}
	deliverInbound(t, b, "spammer@evil.example", "alice@test")
	if junk, inbox := folderCounts(t, mbox); junk != 1 || inbox != 0 {
		t.Errorf("operator allow + user block: junk=%d inbox=%d, want it in Junk (a user block narrows an operator allow)", junk, inbox)
	}
}

// TestRecipientAllowCannotRescueDMARCReject is the per-recipient spoofing guard: a
// recipient's allow must not rescue a message that hard-failed DMARC, because a user
// cannot tell a spoof of the allowed domain from the real sender.
func TestRecipientAllowCannotRescueDMARCReject(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{
		Accounts:        accounts,
		Scorer:          &recordingScorer{verdict: antispam.Verdict{Score: 9, Spam: true, DMARCReject: true}},
		RecipientAccess: fixedRecipAccess{mbox: {"partner.example": antispam.AccessAllow}},
	}
	deliverInbound(t, b, "attacker@partner.example", "alice@test")
	if junk, inbox := folderCounts(t, mbox); junk != 1 || inbox != 0 {
		t.Errorf("user allow + DMARC reject: junk=%d inbox=%d, want it in Junk (an allow cannot rescue a spoof)", junk, inbox)
	}
}

// TestRecipientAllowRescuesToInbox proves a recipient's allow rescues a score-spam
// message (with no operator rule and no DMARC failure) to the inbox.
func TestRecipientAllowRescuesToInbox(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{
		Accounts:        accounts,
		Scorer:          &recordingScorer{verdict: antispam.Verdict{Score: 9, Spam: true}},
		RecipientAccess: fixedRecipAccess{mbox: {"friend.example": antispam.AccessAllow}},
	}
	deliverInbound(t, b, "buddy@friend.example", "alice@test")
	if junk, inbox := folderCounts(t, mbox); junk != 0 || inbox != 1 {
		t.Errorf("user allow: junk=%d inbox=%d, want it rescued to the inbox", junk, inbox)
	}
}

// TestRecipientBlockJunksCleanScore proves a recipient's block files a message that
// scored clean against the global threshold to their Junk.
func TestRecipientBlockJunksCleanScore(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{
		Accounts:        accounts,
		Scorer:          &recordingScorer{verdict: antispam.Verdict{Score: 1, Spam: false}},
		RecipientAccess: fixedRecipAccess{mbox: {"annoying.example": antispam.AccessBlock}},
	}
	deliverInbound(t, b, "spam@annoying.example", "alice@test")
	if junk, inbox := folderCounts(t, mbox); junk != 1 || inbox != 0 {
		t.Errorf("user block: junk=%d inbox=%d, want the clean-scored message junked", junk, inbox)
	}
}

// TestRecipientAccessFilesEachRecipientIndependently is the core of the feature: one
// message, scored once, is filed differently for two recipients — the one who blocked
// the sender gets it in Junk, the one with no rule gets it in the inbox.
func TestRecipientAccessFilesEachRecipientIndependently(t *testing.T) {
	root := t.TempDir()
	aliceMbox := filepath.Join(root, "alice")
	bobMbox := filepath.Join(root, "bob")
	accounts := directory.StaticAccounts{
		"alice@test": {MailboxPath: aliceMbox},
		"bob@test":   {MailboxPath: bobMbox},
	}
	b := &Backend{
		Accounts:        accounts,
		Scorer:          &recordingScorer{verdict: antispam.Verdict{Score: 1, Spam: false}}, // clean for everyone
		RecipientAccess: fixedRecipAccess{aliceMbox: {"sender.example": antispam.AccessBlock}},
	}
	deliverInbound(t, b, "x@sender.example", "alice@test", "bob@test")
	if junk, inbox := folderCounts(t, aliceMbox); junk != 1 || inbox != 0 {
		t.Errorf("alice (blocked the sender): junk=%d inbox=%d, want it junked", junk, inbox)
	}
	if junk, inbox := folderCounts(t, bobMbox); junk != 0 || inbox != 1 {
		t.Errorf("bob (no rule): junk=%d inbox=%d, want it in the inbox", junk, inbox)
	}
}

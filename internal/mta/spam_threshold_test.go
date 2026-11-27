package mta

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/antispam"
	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/smtp"
)

type errThresholds struct{}

func (errThresholds) SpamThresholdForMaildir(string) (int, bool, error) {
	return 0, false, errors.New("threshold read failed")
}

// fixedThresholds is a per-maildir spam-threshold override map; a maildir absent from
// the map has no override.
type fixedThresholds map[string]int

func (f fixedThresholds) SpamThresholdForMaildir(maildir string) (int, bool, error) {
	th, ok := f[maildir]
	return th, ok, nil
}

// deliverInbound runs one unauthenticated inbound message through the backend to the
// given recipients.
func deliverInbound(t *testing.T, b *Backend, from string, rcpts ...string) {
	t.Helper()
	sess, err := b.NewSession("203.0.113.9:1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Mail(from, smtp.MailParams{}); err != nil {
		t.Fatal(err)
	}
	for _, r := range rcpts {
		if err := sess.Rcpt(r, smtp.RcptParams{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.Data(strings.NewReader("From: " + from + "\r\nSubject: x\r\n\r\nbody")); err != nil {
		t.Fatal(err)
	}
}

// folderCounts returns how many messages a mailbox has in its Junk and inbox folders.
func folderCounts(t *testing.T, mbox string) (junk, inbox int) {
	t.Helper()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	j, err := st.ListMessages(int64(mapi.PrivateFIDJunk))
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	return len(j), len(in)
}

// TestPerRecipientThresholdDoesNotOverrideBlock is the discriminating test: a
// blocklisted sender (an access-forced verdict) is filed to Junk even for a recipient
// with a sky-high threshold that would otherwise rescue a score-driven verdict. Were
// the threshold applied to an access-forced verdict, a deliberate block would be
// silently undone.
func TestPerRecipientThresholdDoesNotOverrideBlock(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{
		Accounts:   accounts,
		Scorer:     &recordingScorer{verdict: antispam.Verdict{Score: 3, Spam: true, AccessMatched: true, AccessAction: antispam.AccessBlock}},
		Thresholds: fixedThresholds{mbox: 1000},
	}
	deliverInbound(t, b, "spammer@evil.example", "alice@test")
	if junk, inbox := folderCounts(t, mbox); junk != 1 || inbox != 0 {
		t.Errorf("blocklisted message: junk=%d inbox=%d, want it in Junk despite the high threshold", junk, inbox)
	}
}

// TestPerRecipientHighThresholdRescuesToInbox proves a recipient's high threshold
// files a score-spam message (not access-forced) to the inbox instead of Junk.
func TestPerRecipientHighThresholdRescuesToInbox(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{
		Accounts:   accounts,
		Scorer:     &recordingScorer{verdict: antispam.Verdict{Score: 10, Spam: true}},
		Thresholds: fixedThresholds{mbox: 1000},
	}
	deliverInbound(t, b, "bob@external.example", "alice@test")
	if junk, inbox := folderCounts(t, mbox); junk != 0 || inbox != 1 {
		t.Errorf("high-threshold recipient: junk=%d inbox=%d, want it rescued to the inbox", junk, inbox)
	}
}

// TestPerRecipientLowThresholdJunksCleanScore proves a recipient's low threshold
// files a message that scored clean against the global threshold to Junk.
func TestPerRecipientLowThresholdJunksCleanScore(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	b := &Backend{
		Accounts:   accounts,
		Scorer:     &recordingScorer{verdict: antispam.Verdict{Score: 3, Spam: false}},
		Thresholds: fixedThresholds{mbox: 2},
	}
	deliverInbound(t, b, "bob@external.example", "alice@test")
	if junk, inbox := folderCounts(t, mbox); junk != 1 || inbox != 0 {
		t.Errorf("low-threshold recipient: junk=%d inbox=%d, want the clean-scored message junked", junk, inbox)
	}
}

// TestPerRecipientThresholdFilesEachRecipientIndependently is the core of the
// feature: one message, scored once, is filed differently for two recipients, the
// one with a low override gets it in Junk, the one inheriting the global threshold
// gets it in the inbox.
func TestPerRecipientThresholdFilesEachRecipientIndependently(t *testing.T) {
	root := t.TempDir()
	aliceMbox := filepath.Join(root, "alice")
	bobMbox := filepath.Join(root, "bob")
	accounts := directory.StaticAccounts{
		"alice@test": {MailboxPath: aliceMbox},
		"bob@test":   {MailboxPath: bobMbox},
	}
	b := &Backend{
		Accounts:   accounts,
		Scorer:     &recordingScorer{verdict: antispam.Verdict{Score: 5, Spam: false}}, // ham at the global threshold
		Thresholds: fixedThresholds{aliceMbox: 2},                                      // alice junks at 2; bob inherits
	}
	deliverInbound(t, b, "bob@external.example", "alice@test", "bob@test")

	wantFolderCounts(t, "alice (override 2)", aliceMbox, 1, 0)
	wantFolderCounts(t, "bob (inherits global)", bobMbox, 0, 1)

	// Exact boundary: Score == override -> Junk; Score == override-1 -> Inbox.
	eqMbox := filepath.Join(t.TempDir(), "eq")
	ltMbox := filepath.Join(t.TempDir(), "lt")
	bBound := &Backend{
		Accounts:   directory.StaticAccounts{"eq@test": {MailboxPath: eqMbox}, "lt@test": {MailboxPath: ltMbox}},
		Scorer:     &recordingScorer{verdict: antispam.Verdict{Score: 5, Spam: false}},
		Thresholds: fixedThresholds{eqMbox: 5, ltMbox: 6},
	}
	deliverInbound(t, bBound, "bob@external.example", "eq@test", "lt@test")
	wantFolderCounts(t, "exact threshold score=5 override=5", eqMbox, 1, 0)
	wantFolderCounts(t, "below threshold score=5 override=6", ltMbox, 0, 1)

	// Lookup error fails open to global verdict and logs settings.read.fail.
	errMbox := filepath.Join(t.TempDir(), "err")
	sink := &captureSink{}
	bErr := &Backend{
		Accounts:   directory.StaticAccounts{"err@test": {MailboxPath: errMbox}},
		Scorer:     &recordingScorer{verdict: antispam.Verdict{Score: 9, Spam: true}},
		Thresholds: errThresholds{},
		Logger:     logging.New(sink),
	}
	deliverInbound(t, bErr, "bob@external.example", "err@test")
	wantFolderCounts(t, "threshold read error fail-open", errMbox, 1, 0)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	e, ok := findEvent(sink.events, "settings.read.fail")
	if !ok || e.Fields["settings"] != "spam_threshold" {
		t.Errorf("expected spam_threshold settings.read.fail, got %+v (ok=%v)", e, ok)
	}
}

func wantFolderCounts(t *testing.T, label, mbox string, wantJunk, wantInbox int) {
	t.Helper()
	junk, inbox := folderCounts(t, mbox)
	wantEq(t, label+" junk", junk, wantJunk)
	wantEq(t, label+" inbox", inbox, wantInbox)
}

package meeting

import (
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

const (
	bossAddr      = "boss@hermex.test"
	assistantAddr = "assistant@hermex.test"
)

// bossMailbox provisions the boss's mailbox with the given send-on-behalf grants
// and returns its directory.
func bossMailbox(t *testing.T, onBehalf []string) string {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSendOnBehalf(onBehalf); err != nil {
		t.Fatal(err)
	}
	return dir
}

// delegateAccounts resolves the boss, the assistant and the organizer.
func delegateAccounts(t *testing.T, bossDir string, organizer *objectstore.Store) directory.Accounts {
	t.Helper()
	return directory.StaticAccounts{
		bossAddr:                {MailboxPath: bossDir},
		assistantAddr:           {MailboxPath: t.TempDir()},
		"organizer@hermex.test": {MailboxPath: organizer.Dir()},
	}
}

// deliverAttendeeLine appends an iTIP REPLY for uid whose ATTENDEE line is line, and
// returns the id the delivery pass would hand the processor.
func deliverAttendeeLine(t *testing.T, st *objectstore.Store, uid, line string) int64 {
	t.Helper()
	raw := "From: someone@hermex.test\r\nTo: organizer@hermex.test\r\nSubject: Accepted\r\n" +
		"Content-Type: text/calendar; method=REPLY\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\n" +
		line + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1718200000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// TestDelegateReplyNeedsTheGrant records a delegate's answer on the organizer's
// meeting. A delegate may answer for an attendee while the attendee's mailbox grants
// it the right to send for it, whether the reply names it in SENT-BY or, as Exchange
// sends one, only in the message's Sender; naming itself in SENT-BY proves nothing
// on its own. Before, only the attendee's own reply was recorded, so a delegate's
// answer left the organizer's tracking unchanged.
func TestDelegateReplyNeedsTheGrant(t *testing.T) {
	const uid = "delegate-2"
	line := `ATTENDEE;SENT-BY="mailto:` + assistantAddr + `";PARTSTAT=ACCEPTED:mailto:` + bossAddr
	cases := []struct {
		name     string
		onBehalf []string
		sender   string
		line     string
		want     bool
	}{
		{"granted, named in SENT-BY", []string{assistantAddr}, assistantAddr, line, true},
		{"granted, named in the Sender only", []string{assistantAddr}, assistantAddr,
			"ATTENDEE;PARTSTAT=ACCEPTED:mailto:" + bossAddr, true},
		{"no grant", nil, assistantAddr, line, false},
		{"SENT-BY names someone else", []string{assistantAddr, "carol@hermex.test"}, "carol@hermex.test", line, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			organizer, tags, recipID := organizerWithEvent(t, uid, bossAddr)
			accounts := delegateAccounts(t, bossMailbox(t, c.onBehalf), organizer)
			msgID := deliverAttendeeLine(t, organizer, uid, c.line)

			handled, err := ProcessReply(organizer, accounts, c.sender, msgID)
			if err != nil || handled != c.want {
				t.Fatalf("handled = %v (err %v), want %v", handled, err, c.want)
			}
			want := int32(0)
			if c.want {
				want = ResponseAccepted
			}
			if got := responseOf(t, organizer, tags, recipID); got != want {
				t.Errorf("boss's tracking status = %d, want %d", got, want)
			}
		})
	}
}

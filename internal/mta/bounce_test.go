package mta

import (
	"bytes"
	"io"
	"mime/quotedprintable"
	"net/mail"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	hmime "hermex/internal/mime"
	"hermex/internal/objectstore"
)

// TestBounceMessage proves the non-delivery report is a valid message addressed
// back to the sender, marked auto-generated so it cannot loop, from a
// mailer-daemon origin, naming the failed recipient and the reason.
func TestBounceMessage(t *testing.T) {
	raw := Bounce("alice@local", "bob@remote", "550 mailbox does not exist", time.Unix(1_700_000_000, 0))
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("bounce is not a valid message: %v", err)
	}
	if got := msg.Header.Get("To"); got != "alice@local" {
		t.Errorf("To = %q, want alice@local", got)
	}
	if got := msg.Header.Get("Auto-Submitted"); got != "auto-generated" {
		t.Errorf("Auto-Submitted = %q, want auto-generated (RFC 3834 loop break)", got)
	}
	if from := msg.Header.Get("From"); !strings.Contains(from, "mailer-daemon@local") {
		t.Errorf("From = %q, want a mailer-daemon origin", from)
	}
	// The mailer-daemon origin must be a role mailbox the auto-reply pass skips,
	// or delivering the bounce could provoke a reply loop.
	if !isRoleMailbox("mailer-daemon@local") {
		t.Error("the bounce origin is not recognized as a role mailbox")
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if !bytes.Contains(body, []byte("bob@remote")) || !bytes.Contains(body, []byte("550 mailbox does not exist")) {
		t.Errorf("bounce body missing the recipient or reason: %q", body)
	}
}

// TestBounceDeliversToSenderInbox proves the bounce, filed through the local
// delivery path, lands in the sender's mailbox — the path the relay uses so a
// failed external send is reported to the user, not lost silently.
func TestBounceDeliversToSenderInbox(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@local": {MailboxPath: mbox}}

	raw := Bounce("alice@local", "bob@remote", "host unreachable", time.Now())
	unresolved, err := Deliver(accounts, "", []string{"alice@local"}, raw, time.Now())
	if err != nil {
		t.Fatalf("deliver bounce: %v", err)
	}
	if len(unresolved) != 0 {
		t.Fatalf("the bounce sender did not resolve locally: %v", unresolved)
	}

	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox := int64(mapi.PrivateFIDInbox)
	msgs, err := st.ListMessages(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("sender inbox has %d messages, want the bounce", len(msgs))
	}
	got, err := st.GetMessageRaw(inbox, msgs[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	env, err := hmime.ParseEnvelope(got)
	if err != nil {
		t.Fatalf("parse delivered bounce: %v", err)
	}
	if env.Subject != "Undelivered Mail Returned to Sender" {
		t.Errorf("delivered bounce subject = %q", env.Subject)
	}
}

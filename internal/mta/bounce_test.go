package mta

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
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

// TestBounceMessage proves the non-delivery report is a valid RFC 3464
// multipart/report addressed back to the sender, marked auto-generated so it
// cannot loop, from a mailer-daemon origin, with a human-readable part naming the
// failed recipient and reason and a machine-readable message/delivery-status part
// carrying the structured failure a client parses.
func TestBounceMessage(t *testing.T) {
	raw := Bounce("mail.hermex.test", "alice@local", "bob@remote", "550 mailbox does not exist", time.Unix(1_700_000_000, 0))
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

	// It is a multipart/report; report-type=delivery-status (RFC 3464).
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/report" {
		t.Fatalf("Content-Type = %q, want multipart/report", msg.Header.Get("Content-Type"))
	}
	if params["report-type"] != "delivery-status" {
		t.Errorf("report-type = %q, want delivery-status", params["report-type"])
	}

	// A human-readable text/plain part and a machine-readable
	// message/delivery-status part; a client parses the latter for the failure.
	var human, status string
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("malformed multipart/report: %v", err)
		}
		b, _ := io.ReadAll(p)
		switch ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type")); ct {
		case "text/plain":
			human = string(b)
		case "message/delivery-status":
			status = string(b)
		}
	}
	if !strings.Contains(human, "bob@remote") || !strings.Contains(human, "550 mailbox does not exist") {
		t.Errorf("human-readable part missing the recipient or reason: %q", human)
	}
	for _, want := range []string{
		"Reporting-MTA: dns;mail.hermex.test",
		"Final-Recipient: rfc822;bob@remote",
		"Action: failed",
		"Status: 5.0.0",
		"Diagnostic-Code: smtp; 550 mailbox does not exist",
	} {
		if !strings.Contains(status, want) {
			t.Errorf("delivery-status part missing %q:\n%s", want, status)
		}
	}
}

// TestBounceDeliversToSenderInbox proves the bounce, filed through the local
// delivery path, lands in the sender's mailbox — the path the relay uses so a
// failed external send is reported to the user, not lost silently.
func TestBounceDeliversToSenderInbox(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@local": {MailboxPath: mbox}}

	raw := Bounce("mail.hermex.test", "alice@local", "bob@remote", "host unreachable", time.Now())
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

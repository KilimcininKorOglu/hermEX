package mta

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"path/filepath"
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
	raw, err := Bounce("mail.hermex.test", "alice@local", "bob@remote", "550 mailbox does not exist", time.Unix(1_700_000_000, 0))
	mustNoErr(t, "build the bounce", err)
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	mustNoErr(t, "parse the bounce", err)

	wantEq(t, "the To", msg.Header.Get("To"), "alice@local")
	wantEq(t, "the Auto-Submitted loop break (RFC 3834)", msg.Header.Get("Auto-Submitted"), "auto-generated")
	wantContains(t, "the From", msg.Header.Get("From"), "mailer-daemon@local")
	// The mailer-daemon origin must be a role mailbox the auto-reply pass skips,
	// or delivering the bounce could provoke a reply loop.
	if !isRoleMailbox("mailer-daemon@local") {
		t.Error("the bounce origin is not recognized as a role mailbox")
	}

	// It is a multipart/report; report-type=delivery-status (RFC 3464).
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	mustNoErr(t, "parse the Content-Type", err)
	if mediaType != "multipart/report" {
		t.Fatalf("media type = %q, want multipart/report", mediaType)
	}
	wantEq(t, "the report type", params["report-type"], "delivery-status")

	// A human-readable text/plain part and a machine-readable
	// message/delivery-status part; a client parses the latter for the failure.
	parts := reportParts(t, msg.Body, params["boundary"])
	wantContains(t, "the human-readable part", parts["text/plain"], "bob@remote")
	wantContains(t, "the human-readable part", parts["text/plain"], "550 mailbox does not exist")
	for _, want := range []string{
		"Reporting-MTA: dns;mail.hermex.test",
		"Final-Recipient: rfc822;bob@remote",
		"Action: failed",
		"Status: 5.0.0",
		"Diagnostic-Code: smtp; 550 mailbox does not exist",
	} {
		wantContains(t, "the delivery-status part", parts["message/delivery-status"], want)
	}
}

// reportParts reads a multipart/report body into its parts, keyed by media type.
func reportParts(t *testing.T, body io.Reader, boundary string) map[string]string {
	t.Helper()
	parts := map[string]string{}
	mr := multipart.NewReader(body, boundary)
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return parts
		}
		mustNoErr(t, "read the next report part", err)
		b, err := io.ReadAll(p)
		mustNoErr(t, "read the report part body", err)
		ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		parts[ct] = string(b)
	}
}

// TestBounceDeliversToSenderInbox proves the bounce, filed through the local
// delivery path, lands in the sender's mailbox, the path the relay uses so a
// failed external send is reported to the user, not lost silently.
func TestBounceDeliversToSenderInbox(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@local": {MailboxPath: mbox}}

	raw, err := Bounce("mail.hermex.test", "alice@local", "bob@remote", "host unreachable", time.Now())
	if err != nil {
		t.Fatalf("bounce does not build: %v", err)
	}
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

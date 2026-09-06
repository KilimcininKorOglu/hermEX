package mta

import (
	"strings"
	"testing"

	"hermex/internal/objectstore"
	"hermex/internal/smtp"
)

// The MTA tests assert against generated messages and stored state, and one
// delivery carries many small facts at once: the envelope, a handful of headers,
// the filing folder, and the log events. Written as one composite if per
// assertion, a failure names the whole message and leaves the reader to find
// which field moved.
//
// The helpers here name the fact in the failure message, so a test body reads as
// the list of things it pins.

// wantEq fails when got differs from want, naming the fact in the message.
func wantEq[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// mustNoErr fails the test when err is set, naming the operation.
func mustNoErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantContains fails when the message does not carry the given fragment.
func wantContains(t *testing.T, label, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("%s does not carry %q:\n%s", label, want, got)
	}
}

// deliverBody runs one unauthenticated SMTP transaction carrying a specific
// message body, which is the path an inbound message takes: MAIL, RCPT, DATA.
// deliverInbound (spam_threshold_test.go) is the fixed-body variant.
func deliverBody(t *testing.T, b *Backend, remoteAddr, from, to, raw string) {
	t.Helper()
	sess, err := b.NewSession(remoteAddr)
	mustNoErr(t, "open the session", err)
	mustNoErr(t, "MAIL FROM", sess.Mail(from, smtp.MailParams{}))
	mustNoErr(t, "RCPT TO", sess.Rcpt(to, smtp.RcptParams{}))
	mustNoErr(t, "DATA", sess.Data(strings.NewReader(raw)))
}

// folderMessages lists one folder of a mailbox.
func folderMessages(t *testing.T, mbox string, folderID int64) []objectstore.MessageInfo {
	t.Helper()
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	defer st.Close()
	msgs, err := st.ListMessages(folderID)
	mustNoErr(t, "list the folder", err)
	return msgs
}

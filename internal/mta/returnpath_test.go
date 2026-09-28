package mta

import (
	"net/mail"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// forgedReturnPath is a message whose sender wrote two Return-Path fields of its
// own, one of them folded, and whose Disposition-Notification-To names the same
// third party, the shape that asks a reader to send a receipt somewhere else.
const forgedReturnPath = "Return-Path: <victim@x.test>\r\n" +
	"From: Bob <bob@x.test>\r\nTo: alice@local\r\nSubject: hi\r\n" +
	"return-path:\r\n <other@x.test>\r\n" +
	"Disposition-Notification-To: victim@x.test\r\n\r\nbody\r\n"

// filedReturnPaths delivers a message to a fresh mailbox through fn and returns
// the Return-Path fields of the header block the mailbox kept, in header order.
func filedReturnPaths(t *testing.T, fn func(directory.Accounts) ([]string, error)) []string {
	t.Helper()
	mbox := filepath.Join(t.TempDir(), "alice")
	unresolved, err := fn(directory.StaticAccounts{"alice@local": {MailboxPath: mbox}})
	mustNoErr(t, "deliver", err)
	wantEq(t, "the unresolved recipients", len(unresolved), 0)
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list the inbox", err)
	wantEq(t, "the inbox size", len(msgs), 1)
	props, err := st.GetMessageProperties(msgs[0].ID, mapi.PrTransportMessageHeaders)
	mustNoErr(t, "read the filed headers", err)
	v, _ := props.GetAnyCharset(mapi.PrTransportMessageHeaders)
	headers, _ := v.(string)
	msg, err := mail.ReadMessage(strings.NewReader(headers + "\r\n"))
	mustNoErr(t, "parse the filed headers", err)
	return msg.Header["Return-Path"]
}

// TestDeliveryWritesTheEnvelopeSenderAsReturnPath proves a final delivery files
// the envelope sender as the only Return-Path, whatever fields the sender wrote,
// and writes the null sender as <>.
func TestDeliveryWritesTheEnvelopeSenderAsReturnPath(t *testing.T) {
	for _, c := range []struct{ from, want string }{
		{"bob@x.test", "<bob@x.test>"},
		{"", "<>"},
	} {
		got := filedReturnPaths(t, func(a directory.Accounts) ([]string, error) {
			return Deliver(a, c.from, []string{"alice@local"}, []byte(forgedReturnPath), time.Now())
		})
		wantEq(t, "the Return-Path fields for sender "+c.from, strings.Join(got, ","), c.want)
	}
}

// TestDeliverRetrievedKeepsTheSourceReturnPath proves a message retrieved from a
// remote mailbox keeps the Return-Path its source wrote at final delivery.
func TestDeliverRetrievedKeepsTheSourceReturnPath(t *testing.T) {
	raw := "Return-Path: <origin@x.test>\r\nFrom: origin@x.test\r\nSubject: fetched\r\n\r\nbody\r\n"
	got := filedReturnPaths(t, func(a directory.Accounts) ([]string, error) {
		return DeliverRetrieved(a, "alice@local", []byte(raw), time.Now())
	})
	wantEq(t, "the Return-Path fields", strings.Join(got, ","), "<origin@x.test>")
}

// TestReturnPathRefusesAHeaderBreak proves an envelope sender holding a line break
// is not written, so it cannot start a header of its own, while the sender's own
// Return-Path is still removed.
func TestReturnPathRefusesAHeaderBreak(t *testing.T) {
	out := string(withReturnPath([]byte(forgedReturnPath), "a@x.test\r\nBcc: c@x.test"))
	if strings.Contains(strings.ToLower(out), "return-path") || strings.Contains(out, "Bcc:") {
		t.Errorf("the filed copy carries a Return-Path or an injected field:\n%s", out)
	}
	if !strings.HasPrefix(out, "From: Bob <bob@x.test>\r\n") {
		t.Errorf("the header block lost more than the Return-Path fields:\n%s", out)
	}
}

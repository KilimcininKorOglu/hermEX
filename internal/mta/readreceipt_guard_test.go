package mta

import (
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// receiptParties is a reader, the sender of the message it reads, and a third
// party the request can name, each with a local mailbox.
type receiptParties struct {
	accounts                     directory.StaticAccounts
	readerDir, senderDir, notify string
}

func newReceiptParties(t *testing.T) receiptParties {
	t.Helper()
	base := t.TempDir()
	p := receiptParties{
		readerDir: filepath.Join(base, "reader"),
		senderDir: filepath.Join(base, "sender"),
		notify:    filepath.Join(base, "notify"),
	}
	p.accounts = directory.StaticAccounts{
		"reader@hermex.test": {MailboxPath: p.readerDir},
		"sender@hermex.test": {MailboxPath: p.senderDir},
		"notify@hermex.test": {MailboxPath: p.notify},
	}
	return p
}

// deliverRequest delivers to the reader a message from sender@hermex.test with the
// given envelope sender and Disposition-Notification-To, and returns the reader's
// open store and the message id.
func (p receiptParties) deliverRequest(t *testing.T, envelope, dnt string) (*objectstore.Store, int64) {
	t.Helper()
	raw := "From: sender@hermex.test\r\nTo: reader@hermex.test\r\nSubject: read me\r\n" +
		"Disposition-Notification-To: " + dnt + "\r\n\r\nbody\r\n"
	_, err := Deliver(p.accounts, envelope, []string{"reader@hermex.test"}, []byte(raw), time.Now())
	mustNoErr(t, "deliver the request", err)
	msgs := listInbox(t, p.readerDir)
	st, err := objectstore.Open(p.readerDir)
	mustNoErr(t, "open the reader's mailbox", err)
	t.Cleanup(func() { st.Close() })
	return st, msgs[len(msgs)-1].ID
}

// send sends the receipt the message asks for in the given mode.
func (p receiptParties) send(t *testing.T, st *objectstore.Store, id int64, mode ReceiptMode) {
	t.Helper()
	mustNoErr(t, "send the receipt", SendRequestedReceipt(p.accounts, nil, st, id, "reader@hermex.test", mode, time.Now()))
}

// TestAutomaticReceiptNeedsTheReturnPath proves RFC 8098 section 2.1: an automatic
// receipt goes out only when the request names the envelope sender alone, and a
// request it may not answer stays pending for the reader, whose own decision
// sends it.
func TestAutomaticReceiptNeedsTheReturnPath(t *testing.T) {
	for _, c := range []struct {
		name, envelope, dnt string
		sends               bool
	}{
		{"the envelope sender", "sender@hermex.test", "sender@hermex.test", true},
		{"a third party", "sender@hermex.test", "notify@hermex.test", false},
		{"two addresses", "sender@hermex.test", "sender@hermex.test, notify@hermex.test", false},
		{"the null sender", "", "sender@hermex.test", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newReceiptParties(t)
			st, id := p.deliverRequest(t, c.envelope, c.dnt)
			p.send(t, st, id, ReceiptAutomatic)
			sent := len(listInbox(t, p.senderDir)) + len(listInbox(t, p.notify))
			wantEq(t, "automatic receipts", sent == 1, c.sends)
			pending, err := ReceiptPending(st, id)
			mustNoErr(t, "read the request", err)
			wantEq(t, "the request pending afterwards", pending, !c.sends)
		})
	}
}

// TestReceiptTheReaderChoseIsNotGuarded proves a receipt the reader or their
// client decided on goes to the requested address even when it is not the
// Return-Path, and that a message never delivered sends no automatic one.
func TestReceiptTheReaderChoseIsNotGuarded(t *testing.T) {
	p := newReceiptParties(t)
	st, id := p.deliverRequest(t, "sender@hermex.test", "notify@hermex.test")
	p.send(t, st, id, ReceiptManual)
	wantEq(t, "receipts to the named address", len(listInbox(t, p.notify)), 1)
	wantEq(t, "receipts to the sender", len(listInbox(t, p.senderDir)), 0)

	stored, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrReadReceiptRequested, Value: true},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "sender@hermex.test"},
	}})
	mustNoErr(t, "store an undelivered request", err)
	p.send(t, st, stored, ReceiptAutomatic)
	wantEq(t, "automatic receipts for an undelivered message", len(listInbox(t, p.senderDir)), 0)
	p.send(t, st, stored, ReceiptClient)
	wantEq(t, "receipts the client asked for", len(listInbox(t, p.senderDir)), 1)
}

// TestReceiptDestinationOrder proves the receipt goes to the address the request
// names, else to the sender, else to the represented sender.
func TestReceiptDestinationOrder(t *testing.T) {
	for _, c := range []struct {
		name  string
		props mapi.PropertyValues
		want  string
	}{
		{"the read receipt address", mapi.PropertyValues{
			{Tag: mapi.PrReadReceiptSmtpAddress, Value: "notify@hermex.test"},
			{Tag: mapi.PrSenderSmtpAddress, Value: "sender@hermex.test"},
			{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "from@hermex.test"},
		}, "notify@hermex.test"},
		{"the sender", mapi.PropertyValues{
			{Tag: mapi.PrSenderSmtpAddress, Value: "sender@hermex.test"},
			{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "from@hermex.test"},
		}, "sender@hermex.test"},
		{"the represented sender", mapi.PropertyValues{
			{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "from@hermex.test"},
		}, "from@hermex.test"},
	} {
		props := append(mapi.PropertyValues{{Tag: mapi.PrReadReceiptRequested, Value: true}}, c.props...)
		wantEq(t, "the destination for "+c.name, receiptDestination(props), c.want)
	}
	wantEq(t, "the destination without a request",
		receiptDestination(mapi.PropertyValues{{Tag: mapi.PrSenderSmtpAddress, Value: "sender@hermex.test"}}), "")
}

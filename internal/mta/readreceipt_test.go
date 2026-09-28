package mta

import (
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// TestBuildReadReceiptMDN parses a generated read receipt back and asserts the
// load-bearing MDN shape: a multipart/report; report-type=disposition-notification
// with a text/plain part FIRST and a message/disposition-notification part SECOND,
// the DN part carrying Final-Recipient and the "displayed" disposition, and the
// envelope addressed from the reader to the represented sender with the
// X-Auto-Response-Suppress loop guard. Parsing the bytes back, rather than string
// matching, is what catches a boundary bug in the hand-built multipart.
func TestBuildReadReceiptMDN(t *testing.T) {
	when := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	raw, err := buildReadReceipt(ReadReceiptInfo{
		Reader:      "reader@hermex.test",
		To:          "sender@hermex.test",
		OrigFrom:    "sender@hermex.test",
		OrigSubject: "Quarterly numbers",
		OrigMsgID:   "<orig-1@hermex.test>",
		SubmitTime:  when,
	}, when)
	mustNoErr(t, "build the receipt", err)

	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	mustNoErr(t, "parse the receipt as a message", err)

	wantEq(t, "the From (the reader)", msg.Header.Get("From"), "reader@hermex.test")
	wantEq(t, "the To (the represented sender)", msg.Header.Get("To"), "sender@hermex.test")
	wantEq(t, "the X-Auto-Response-Suppress loop guard", msg.Header.Get("X-Auto-Response-Suppress"), "All")
	subj, err := (&mime.WordDecoder{}).DecodeHeader(msg.Header.Get("Subject"))
	mustNoErr(t, "decode the subject", err)
	wantEq(t, "the subject", subj, readReceiptSubject)

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	mustNoErr(t, "parse the Content-Type", err)
	if mediaType != "multipart/report" {
		t.Fatalf("media type = %q, want multipart/report", mediaType)
	}
	wantEq(t, "the report type", params["report-type"], "disposition-notification")

	mr := multipart.NewReader(msg.Body, params["boundary"])

	body1 := nextPart(t, mr, "part 1", "text/plain")
	wantContains(t, "part 1", body1, "reader@hermex.test")
	wantContains(t, "part 1", body1, "Quarterly numbers")

	dn := nextPart(t, mr, "part 2", "message/disposition-notification")
	for _, want := range []string{
		"Final-Recipient: rfc822;reader@hermex.test",
		"Disposition: automatic-action/MDN-sent-automatically; displayed",
		"Original-Message-ID: <orig-1@hermex.test>",
	} {
		wantContains(t, "the disposition-notification", dn, want)
	}

	if _, err := mr.NextPart(); err != io.EOF {
		t.Errorf("want exactly two parts, got a third (err=%v)", err)
	}
}

// nextPart reads the next MIME part, requiring the content type it must carry,
// and returns its body.
func nextPart(t *testing.T, mr *multipart.Reader, what, mediaType string) string {
	t.Helper()
	p, err := mr.NextPart()
	mustNoErr(t, "read "+what, err)
	if ct := p.Header.Get("Content-Type"); !strings.HasPrefix(ct, mediaType) {
		t.Errorf("%s Content-Type = %q, want %s", what, ct, mediaType)
	}
	body, err := io.ReadAll(p)
	mustNoErr(t, "read the body of "+what, err)
	return string(body)
}

// TestBuildReadReceiptManualDisposition proves a receipt the reader chose to send
// reports a manual action, as RFC 8098 section 3.2.6 asks.
func TestBuildReadReceiptManualDisposition(t *testing.T) {
	raw, err := buildReadReceipt(ReadReceiptInfo{Reader: "reader@hermex.test", To: "sender@hermex.test", Manual: true}, time.Now())
	mustNoErr(t, "build the receipt", err)
	wantContains(t, "the manual receipt", string(raw), "Disposition: manual-action/MDN-sent-manually; displayed")
}

// seedRequestedReceipt stores a message in dir that asks for a read receipt from
// sender and returns the open store and the message id.
func seedRequestedReceipt(t *testing.T, dir, sender string) (*objectstore.Store, int64) {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the reader's mailbox", err)
	id, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
		{Tag: mapi.PrSubject, Value: "PingMe"},
		{Tag: mapi.PrReadReceiptRequested, Value: true},
		{Tag: mapi.PrNonReceiptNotificationRequested, Value: true},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: sender},
	}})
	mustNoErr(t, "store the message", err)
	return st, id
}

// TestSendRequestedReceiptSendsOnce proves the shared receipt path delivers one
// receipt and consumes the request, so a second call, from any surface, sends
// nothing, and that a declined request is consumed without a receipt.
func TestSendRequestedReceiptSendsOnce(t *testing.T) {
	readerDir, senderDir := t.TempDir(), t.TempDir()
	accounts := directory.StaticAccounts{
		"reader@hermex.test": {MailboxPath: readerDir},
		"sender@hermex.test": {MailboxPath: senderDir},
	}
	st, id := seedRequestedReceipt(t, readerDir, "sender@hermex.test")
	defer st.Close()

	pending, err := ReceiptPending(st, id)
	mustNoErr(t, "read the request", err)
	wantEq(t, "pending before the read", pending, true)
	for range 2 {
		mustNoErr(t, "send the receipt", SendRequestedReceipt(accounts, nil, st, id, "reader@hermex.test", ReceiptClient, time.Now()))
	}
	wantEq(t, "receipts in the sender's inbox", len(listInbox(t, senderDir)), 1)
	pending, err = ReceiptPending(st, id)
	mustNoErr(t, "read the request", err)
	wantEq(t, "pending after the send", pending, false)

	declined, err := st.CreateMessage(int64(mapi.PrivateFIDInbox), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrReadReceiptRequested, Value: true},
		{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "sender@hermex.test"},
	}})
	mustNoErr(t, "store a second message", err)
	mustNoErr(t, "decline", ConsumeReceiptRequest(st, declined))
	mustNoErr(t, "read after declining", SendRequestedReceipt(accounts, nil, st, declined, "reader@hermex.test", ReceiptClient, time.Now()))
	wantEq(t, "receipts after a decline", len(listInbox(t, senderDir)), 1)
}

// TestBuildReadReceiptOmitsAbsentFields confirms the optional decorations are
// dropped when their source is empty: no Original-Message-ID line without an
// original id, and no Time line without a submit time, the message stays
// well-formed and parseable.
func TestBuildReadReceiptOmitsAbsentFields(t *testing.T) {
	when := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	raw, err := buildReadReceipt(ReadReceiptInfo{
		Reader: "reader@hermex.test",
		To:     "sender@hermex.test",
	}, when)
	if err != nil {
		t.Fatalf("receipt does not build: %v", err)
	}

	if _, err := mail.ReadMessage(strings.NewReader(string(raw))); err != nil {
		t.Fatalf("receipt with absent optional fields does not parse: %v", err)
	}
	if strings.Contains(string(raw), "Original-Message-ID:") {
		t.Errorf("Original-Message-ID emitted with no original id")
	}
	if strings.Contains(string(raw), "Time:") {
		t.Errorf("Time line emitted with no submit time")
	}
}

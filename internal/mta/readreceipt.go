package mta

import (
	"bytes"
	"fmt"
	"mime"
	"mime/multipart"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/relay"
)

// readReceiptSubject is the fixed subject of a read-receipt MDN, matching the
// reference's read-notification template.
const readReceiptSubject = "Your message has been read!"

// ReadReceiptInfo carries the fields a read-receipt MDN needs, extracted by the
// caller from the message being marked read. Reader is the mailbox owner who read
// the message (the MDN From and Final-Recipient); To is the destination, the
// original message's PR_SENT_REPRESENTING_SMTP_ADDRESS. The Orig* fields decorate
// the human-readable part and correlate the notification to the original message.
type ReadReceiptInfo struct {
	Reader      string
	To          string
	OrigFrom    string
	OrigSubject string
	OrigMsgID   string
	SubmitTime  time.Time
	// Manual marks a receipt the reader chose to send when asked, which RFC 8098
	// reports as a manual action rather than an automatic one.
	Manual bool
}

// SendReadReceipt builds an Exchange-style disposition-notification (MDN) for a
// just-read message and delivers it to the message's represented sender. It is
// best-effort like the out-of-office pass, the caller logs the error and never
// fails the read that triggered it.
//
// The MDN is hand-built rather than routed through oxcmail.Export for the same
// reason buildAutoReply is: Export emits a fixed single-part header set and
// cannot produce the multipart/report; report-type=disposition-notification
// structure by which a receiving client recognizes a message as a read receipt.
//
// A sender without a mailbox here is reached through spool, like any mail the
// reader sends; with a nil spool the receipt is delivered locally only.
func SendReadReceipt(accounts directory.Accounts, spool *relay.Spool, info ReadReceiptInfo, when time.Time) error {
	raw, err := buildReadReceipt(info, when)
	if err != nil {
		return err
	}
	if _, err := DeliverAndRelay(accounts, spool, info.Reader, []string{info.To}, raw, when); err != nil {
		return err
	}
	return nil
}

// ReceiptError reports which step of sending a requested read receipt failed:
// "read" (the message's request), "send", or "clear" (consuming the request after
// the send). A failed clear is the one that repeats, since the request still stands
// and the next read sends the receipt again.
type ReceiptError struct {
	Stage string
	Err   error
}

func (e *ReceiptError) Error() string { return "read receipt " + e.Stage + ": " + e.Err.Error() }

func (e *ReceiptError) Unwrap() error { return e.Err }

// receiptRequestTags are the properties a read-receipt request is read from.
var receiptRequestTags = []mapi.PropTag{
	mapi.PrReadReceiptRequested,
	mapi.PrSentRepresentingSmtpAddress,
	mapi.PrSubject,
	mapi.PrInternetMessageID,
	mapi.PrClientSubmitTime,
}

// SendRequestedReceipt sends the read receipt a stored message asks for
// ([MS-OXOMSG] 3.3.4.3) and then consumes the request, so the receipt is sent once
// whichever protocol reads the message next. A message with no pending request, or
// with no represented sender to tell, sends nothing and returns nil. reader is the
// address of the mailbox that read the message; manual marks a receipt the reader
// chose to send rather than one sent on their behalf. Every surface that marks a
// message read calls this one function, so they cannot drift.
func SendRequestedReceipt(accounts directory.Accounts, spool *relay.Spool, st *objectstore.Store, messageID int64, reader string, manual bool, when time.Time) error {
	props, err := st.GetMessageProperties(messageID, receiptRequestTags...)
	if err != nil {
		return &ReceiptError{Stage: "read", Err: err}
	}
	if req, _ := props.Get(mapi.PrReadReceiptRequested); req != true {
		return nil
	}
	dest := stringValue(props, mapi.PrSentRepresentingSmtpAddress)
	if dest == "" {
		return nil
	}
	info := ReadReceiptInfo{
		Reader:      reader,
		To:          dest,
		OrigFrom:    dest,
		OrigSubject: stringValue(props, mapi.PrSubject),
		OrigMsgID:   stringValue(props, mapi.PrInternetMessageID),
		Manual:      manual,
	}
	if v, ok := props.Get(mapi.PrClientSubmitTime); ok {
		if nt, ok := v.(uint64); ok {
			info.SubmitTime = mapi.NTTimeToUnix(nt)
		}
	}
	if err := SendReadReceipt(accounts, spool, info, when); err != nil {
		return &ReceiptError{Stage: "send", Err: err}
	}
	if err := ConsumeReceiptRequest(st, messageID); err != nil {
		return &ReceiptError{Stage: "clear", Err: err}
	}
	return nil
}

// ConsumeReceiptRequest clears a message's read-receipt and non-read notification
// requests without sending anything, for a reader who declined, so no later read on
// any protocol sends the receipt.
func ConsumeReceiptRequest(st *objectstore.Store, messageID int64) error {
	return st.SetMessageProperties(messageID, mapi.PropertyValues{
		{Tag: mapi.PrReadReceiptRequested, Value: false},
		{Tag: mapi.PrNonReceiptNotificationRequested, Value: false},
	})
}

// ReceiptPending reports whether a stored message still asks for a read receipt
// that can be sent: the request is set and there is a represented sender to tell.
func ReceiptPending(st *objectstore.Store, messageID int64) (bool, error) {
	props, err := st.GetMessageProperties(messageID, mapi.PrReadReceiptRequested, mapi.PrSentRepresentingSmtpAddress)
	if err != nil {
		return false, err
	}
	req, _ := props.Get(mapi.PrReadReceiptRequested)
	return req == true && stringValue(props, mapi.PrSentRepresentingSmtpAddress) != "", nil
}

// stringValue reads a string property, empty when it is absent.
func stringValue(props mapi.PropertyValues, tag mapi.PropTag) string {
	v, _ := props.GetAnyCharset(tag)
	s, _ := v.(string)
	return s
}

// buildReadReceipt assembles the multipart/report MDN: a human-readable
// text/plain part followed by a message/disposition-notification part, wrapped in
// the RFC 5322 envelope addressed from the reader to the represented sender. The
// loop guard is X-Auto-Response-Suppress: All, the header Exchange uses to stop
// an auto-responder replying to the receipt (distinct from buildAutoReply's
// Auto-Submitted, which the reference does not set on a receipt).
func buildReadReceipt(info ReadReceiptInfo, when time.Time) ([]byte, error) {
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	if err := writeReportPart(mw, "text/plain; charset=utf-8", []byte(readReceiptText(info))); err != nil {
		return nil, err
	}
	if err := writeReportPart(mw, "message/disposition-notification", []byte(dispositionNotification(info))); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var msg bytes.Buffer
	writeReplyField(&msg, "From", info.Reader)
	writeReplyField(&msg, "To", info.To)
	writeReplyField(&msg, "Subject", mime.QEncoding.Encode("utf-8", readReceiptSubject))
	writeReplyField(&msg, "Date", when.UTC().Format(dateLayout))
	writeReplyField(&msg, "Message-ID", "<"+newToken()+"@"+domainOf(info.Reader)+">")
	writeReplyField(&msg, "X-Auto-Response-Suppress", "All")
	writeReplyField(&msg, "MIME-Version", "1.0")
	writeReplyField(&msg, "Content-Type",
		`multipart/report; report-type="disposition-notification"; boundary="`+mw.Boundary()+`"`)
	msg.WriteString("\r\n")
	msg.Write(parts.Bytes())
	return msg.Bytes(), nil
}

// readReceiptText is the human-readable part, modeled on the reference's
// read-notification template: it states the message was displayed to the reader
// and echoes the original sender and subject. The reference's optional recipient
// and length lines, which need the original recipient table and size, are omitted
// as non-load-bearing.
func readReceiptText(info ReadReceiptInfo) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Your message has been displayed to %q.\r\n\r\n", info.Reader)
	b.WriteString("Below is the original mail information:\r\n")
	if !info.SubmitTime.IsZero() {
		fmt.Fprintf(&b, "   Time:    %s\r\n", info.SubmitTime.UTC().Format(dateLayout))
	}
	if info.OrigFrom != "" {
		fmt.Fprintf(&b, "   From:    %s\r\n", info.OrigFrom)
	}
	if info.OrigSubject != "" {
		fmt.Fprintf(&b, "   Subject: %s\r\n", info.OrigSubject)
	}
	return b.String()
}

// dispositionNotification is the machine-readable message/disposition-notification
// part ([RFC 8098] / [MS-OXOMSG]): the reader is the Final-Recipient and the
// disposition is a "displayed" (read) notification, sent automatically or, when
// the reader agreed to a prompt, manually.
func dispositionNotification(info ReadReceiptInfo) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Final-Recipient: rfc822;%s\r\n", info.Reader)
	if info.Manual {
		b.WriteString("Disposition: manual-action/MDN-sent-manually; displayed\r\n")
	} else {
		b.WriteString("Disposition: automatic-action/MDN-sent-automatically; displayed\r\n")
	}
	if info.OrigMsgID != "" {
		fmt.Fprintf(&b, "Original-Message-ID: %s\r\n", info.OrigMsgID)
	}
	return b.String()
}

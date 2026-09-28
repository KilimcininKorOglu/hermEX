package mta

import (
	"bytes"
	"fmt"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
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
// the message (the MDN From and Final-Recipient); To is the destination the
// request names (receiptDestination). The Orig* fields decorate the human-readable
// part and correlate the notification to the original message, OrigFrom being its
// From address.
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
// just-read message and delivers it to info.To. It is best-effort like the
// out-of-office pass, the caller logs the error and never fails the read that
// triggered it.
//
// The MDN is hand-built rather than routed through oxcmail.Export for the same
// reason buildAutoReply is: Export emits a fixed single-part header set and
// cannot produce the multipart/report; report-type=disposition-notification
// structure by which a receiving client recognizes a message as a read receipt.
//
// A destination without a mailbox here is reached through spool, like any mail
// the reader sends; with a nil spool the receipt is delivered locally only.
func SendReadReceipt(accounts directory.Accounts, spool *relay.Spool, info ReadReceiptInfo, when time.Time) error {
	raw, err := buildReadReceipt(info, when)
	if err != nil {
		return err
	}
	if _, err := SendReport(accounts, spool, info.Reader, []string{info.To}, raw, when); err != nil {
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

// ReceiptMode says who decided that a read receipt goes out.
type ReceiptMode int

const (
	// ReceiptAutomatic is a receipt the server sends for the reader by a standing
	// setting, without asking them.
	ReceiptAutomatic ReceiptMode = iota
	// ReceiptClient is a receipt a client asked for when it marked the message read
	// without suppressing it, having applied its own setting or prompt.
	ReceiptClient
	// ReceiptManual is a receipt the reader chose to send when asked.
	ReceiptManual
)

// receiptAddressTags are the addresses a read receipt can go to, in the order
// receiptDestination prefers them.
var receiptAddressTags = []mapi.PropTag{
	mapi.PrReadReceiptSmtpAddress,
	mapi.PrSenderSmtpAddress,
	mapi.PrSentRepresentingSmtpAddress,
}

// receiptDestinationTags are the properties that say whether a message asks for a
// read receipt and where it goes.
var receiptDestinationTags = append([]mapi.PropTag{mapi.PrReadReceiptRequested}, receiptAddressTags...)

// receiptRequestTags are the properties a read-receipt request is read from.
var receiptRequestTags = append([]mapi.PropTag{
	mapi.PrSubject,
	mapi.PrInternetMessageID,
	mapi.PrClientSubmitTime,
	mapi.PrTransportMessageHeaders,
}, receiptDestinationTags...)

// SendRequestedReceipt sends the read receipt a stored message asks for
// ([MS-OXOMSG] 3.3.4.3) and then consumes the request, so the receipt is sent once
// whichever protocol reads the message next. A message with no pending request, or
// with nobody to tell, sends nothing and returns nil, and so does an automatic
// receipt RFC 8098 does not allow, which leaves the request for the reader to
// answer. reader is the address of the mailbox that read the message. Every
// surface that marks a message read calls this one function, so they cannot drift.
func SendRequestedReceipt(accounts directory.Accounts, spool *relay.Spool, st *objectstore.Store, messageID int64, reader string, mode ReceiptMode, when time.Time) error {
	props, err := st.GetMessageProperties(messageID, receiptRequestTags...)
	if err != nil {
		return &ReceiptError{Stage: "read", Err: err}
	}
	dest := receiptDestination(props)
	if dest == "" || (mode == ReceiptAutomatic && !automaticReceiptAllowed(props, dest)) {
		return nil
	}
	info := ReadReceiptInfo{
		Reader:      reader,
		To:          dest,
		OrigFrom:    stringValue(props, mapi.PrSentRepresentingSmtpAddress),
		OrigSubject: stringValue(props, mapi.PrSubject),
		OrigMsgID:   stringValue(props, mapi.PrInternetMessageID),
		Manual:      mode == ReceiptManual,
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
// that can be sent: the request is set and it names somebody to tell.
func ReceiptPending(st *objectstore.Store, messageID int64) (bool, error) {
	props, err := st.GetMessageProperties(messageID, receiptDestinationTags...)
	if err != nil {
		return false, err
	}
	return receiptDestination(props) != "", nil
}

// receiptDestination is the address a message's read receipt goes to, empty when
// it asks for none or names nobody. [MS-OXOMSG] sends the receipt to the read
// receipt address the request names, else to the sender, so a receipt asked for
// with a Disposition-Notification-To goes there, not to the From address. The
// represented sender is the last resort for a message that names no sender.
func receiptDestination(props mapi.PropertyValues) string {
	if req, _ := props.Get(mapi.PrReadReceiptRequested); req != true {
		return ""
	}
	for _, tag := range receiptAddressTags {
		if addr := stringValue(props, tag); addr != "" {
			return addr
		}
	}
	return ""
}

// automaticReceiptAllowed applies RFC 8098 section 2.1 to a receipt sent without
// asking the reader: it goes out only when the request names one address and that
// address is the Return-Path the final delivery wrote. The request can name anyone
// the sender chose, and an automatic receipt to such an address would let a sender
// have this server mail a third party on every read; the reader has to agree to
// that one. A message with no Return-Path, such as one never delivered, sends no
// automatic receipt either.
func automaticReceiptAllowed(props mapi.PropertyValues, dest string) bool {
	hdr, err := mail.ReadMessage(strings.NewReader(stringValue(props, mapi.PrTransportMessageHeaders) + "\r\n"))
	if err != nil || !singleNotificationAddress(hdr.Header) {
		return false
	}
	// The final delivery writes its Return-Path first and removes every other.
	rp, err := mail.ParseAddress(hdr.Header.Get("Return-Path"))
	return err == nil && strings.EqualFold(rp.Address, dest)
}

// singleNotificationAddress reports whether the Disposition-Notification-To fields
// name no more than one distinct address, and parse. RFC 8098 section 2.1 has a
// reader confirm a request that names more than one.
func singleNotificationAddress(h mail.Header) bool {
	fields := h["Disposition-Notification-To"]
	if len(fields) == 0 {
		return true
	}
	list, err := mail.ParseAddressList(strings.Join(fields, ", "))
	if err != nil {
		return false
	}
	for _, a := range list[1:] {
		if !strings.EqualFold(a.Address, list[0].Address) {
			return false
		}
	}
	return true
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

package mailreport

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"hermex/internal/mime"
)

// Failure is one DMARC failure report (RFC 6591 over RFC 5965): the facts about a
// single message that failed authentication for the reported domain.
//
// OriginalHeaders is the failed message's header block. Its body is never kept:
// the report exists to diagnose authentication, and the body is someone's mail.
type Failure struct {
	ArrivalDate           time.Time
	SourceIP              string
	ReportedDomain        string
	AuthFailure           string
	OriginalMailFrom      string
	OriginalRcptTo        string
	DKIMDomain            string
	DeliveryResult        string
	AuthenticationResults string
	OriginalHeaders       string
}

// isFeedbackReport reports whether the message is an RFC 5965 feedback report.
func isFeedbackReport(p *mime.Part) bool {
	return p.Type == "multipart" && p.Subtype == "report" &&
		strings.EqualFold(p.Params["report-type"], "feedback-report")
}

// parseFailure reads a feedback report. Only the "auth-failure" feedback type is a
// DMARC failure report; an abuse complaint in the same format is not one.
func parseFailure(root *mime.Part) (Result, error) {
	fields, err := feedbackFields(root)
	if err != nil {
		return Result{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(fields.Get("Feedback-Type")), "auth-failure") {
		return Result{}, ErrNotReport
	}
	headers, err := originalHeaders(root)
	if err != nil {
		return Result{}, err
	}
	f := &Failure{
		SourceIP:              fields.Get("Source-Ip"),
		ReportedDomain:        fields.Get("Reported-Domain"),
		AuthFailure:           fields.Get("Auth-Failure"),
		OriginalMailFrom:      fields.Get("Original-Mail-From"),
		OriginalRcptTo:        strings.Join(fields.Values("Original-Rcpt-To"), ", "),
		DKIMDomain:            fields.Get("Dkim-Domain"),
		DeliveryResult:        fields.Get("Delivery-Result"),
		AuthenticationResults: strings.Join(fields.Values("Authentication-Results"), "\n"),
		OriginalHeaders:       headers,
	}
	if d, err := mail.ParseDate(fields.Get("Arrival-Date")); err == nil {
		f.ArrivalDate = d.UTC()
	}
	return Result{Kind: KindDMARCFailure, Failure: f}, nil
}

// feedbackFields reads the machine-readable part of a feedback report.
func feedbackFields(root *mime.Part) (textproto.MIMEHeader, error) {
	for _, c := range root.Children {
		if c.Type == "message" && c.Subtype == "feedback-report" {
			body, err := c.DecodedContent()
			if err != nil {
				return nil, err
			}
			return readFields(body)
		}
	}
	return nil, ErrNotReport
}

// readFields parses a block of header-style fields. The block is copied before the
// terminating blank line is added, because body is a slice of the whole message
// and appending to it in place would overwrite the bytes that follow it.
func readFields(body []byte) (textproto.MIMEHeader, error) {
	if len(body) > maxHeaderBytes {
		return nil, ErrTooLarge
	}
	buf := make([]byte, 0, len(body)+4)
	buf = append(buf, body...)
	buf = append(buf, "\r\n\r\n"...)
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(buf))).ReadMIMEHeader()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("mailreport: feedback report fields: %w", err)
	}
	return h, nil
}

// originalHeaders returns the failed message's header block, from either form a
// reporter may attach: the whole message, or its headers alone. It is empty when
// the reporter attached neither, which RFC 6591 allows.
func originalHeaders(root *mime.Part) (string, error) {
	for _, c := range root.Children {
		switch {
		case c.Type == "message" && c.Subtype == "rfc822" && c.MsgBody != nil:
			return capHeaders(c.MsgBody.RawHeader()), nil
		case c.Type == "text" && c.Subtype == "rfc822-headers":
			b, err := c.DecodedContent()
			if err != nil {
				return "", err
			}
			return capHeaders(b), nil
		}
	}
	return "", nil
}

// capHeaders bounds the kept header block at maxHeaderBytes. The block is kept for
// reading, not for replay, so a cut that ends mid-line is acceptable; an invalid
// UTF-8 sequence the cut leaves behind is dropped.
func capHeaders(b []byte) string {
	if len(b) > maxHeaderBytes {
		b = b[:maxHeaderBytes]
	}
	return strings.ToValidUTF8(string(b), "")
}

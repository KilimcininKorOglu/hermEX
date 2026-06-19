package mta

import (
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// TestBuildReadReceiptMDN parses a generated read receipt back and asserts the
// load-bearing MDN shape: a multipart/report; report-type=disposition-notification
// with a text/plain part FIRST and a message/disposition-notification part SECOND,
// the DN part carrying Final-Recipient and the "displayed" disposition, and the
// envelope addressed from the reader to the represented sender with the
// X-Auto-Response-Suppress loop guard. Parsing the bytes back — rather than string
// matching — is what catches a boundary bug in the hand-built multipart.
func TestBuildReadReceiptMDN(t *testing.T) {
	when := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	raw := buildReadReceipt(ReadReceiptInfo{
		Reader:      "reader@hermex.test",
		To:          "sender@hermex.test",
		OrigFrom:    "sender@hermex.test",
		OrigSubject: "Quarterly numbers",
		OrigMsgID:   "<orig-1@hermex.test>",
		SubmitTime:  when,
	}, when)

	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("receipt does not parse as a message: %v", err)
	}

	if got := msg.Header.Get("From"); got != "reader@hermex.test" {
		t.Errorf("From = %q, want the reader", got)
	}
	if got := msg.Header.Get("To"); got != "sender@hermex.test" {
		t.Errorf("To = %q, want the represented sender", got)
	}
	if got := msg.Header.Get("X-Auto-Response-Suppress"); got != "All" {
		t.Errorf("X-Auto-Response-Suppress = %q, want All (the loop guard)", got)
	}
	if subj, err := (&mime.WordDecoder{}).DecodeHeader(msg.Header.Get("Subject")); err != nil || subj != readReceiptSubject {
		t.Errorf("Subject = %q (err %v), want %q", msg.Header.Get("Subject"), err, readReceiptSubject)
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("Content-Type parse: %v", err)
	}
	if mediaType != "multipart/report" {
		t.Fatalf("media type = %q, want multipart/report", mediaType)
	}
	if rt := params["report-type"]; rt != "disposition-notification" {
		t.Errorf("report-type = %q, want disposition-notification", rt)
	}

	mr := multipart.NewReader(msg.Body, params["boundary"])

	p1, err := mr.NextPart()
	if err != nil {
		t.Fatalf("first part: %v", err)
	}
	if ct := p1.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("part 1 Content-Type = %q, want text/plain first", ct)
	}
	body1, _ := io.ReadAll(p1)
	if !strings.Contains(string(body1), "reader@hermex.test") || !strings.Contains(string(body1), "Quarterly numbers") {
		t.Errorf("part 1 body missing reader or original subject: %q", body1)
	}

	p2, err := mr.NextPart()
	if err != nil {
		t.Fatalf("second part: %v", err)
	}
	if ct := p2.Header.Get("Content-Type"); !strings.HasPrefix(ct, "message/disposition-notification") {
		t.Errorf("part 2 Content-Type = %q, want message/disposition-notification second", ct)
	}
	dn, _ := io.ReadAll(p2)
	for _, want := range []string{
		"Final-Recipient: rfc822;reader@hermex.test",
		"Disposition: automatic-action/MDN-sent-automatically; displayed",
		"Original-Message-ID: <orig-1@hermex.test>",
	} {
		if !strings.Contains(string(dn), want) {
			t.Errorf("disposition-notification missing %q in:\n%s", want, dn)
		}
	}

	if _, err := mr.NextPart(); err != io.EOF {
		t.Errorf("want exactly two parts, got a third (err=%v)", err)
	}
}

// TestBuildReadReceiptOmitsAbsentFields confirms the optional decorations are
// dropped when their source is empty: no Original-Message-ID line without an
// original id, and no Time line without a submit time — the message stays
// well-formed and parseable.
func TestBuildReadReceiptOmitsAbsentFields(t *testing.T) {
	when := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	raw := buildReadReceipt(ReadReceiptInfo{
		Reader: "reader@hermex.test",
		To:     "sender@hermex.test",
	}, when)

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

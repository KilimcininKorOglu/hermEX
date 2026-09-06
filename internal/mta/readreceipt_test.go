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

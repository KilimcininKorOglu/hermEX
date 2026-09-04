package oxcmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// injectedHeaderLine reports whether any line of the message starts a header of
// the given name. Injection means a new header line (or a premature blank line
// ending the header block); the same text left inert inside a header value is
// not, so the check is structural rather than a substring search.
func injectedHeaderLine(out []byte, name string) bool {
	for line := range strings.SplitSeq(string(out), "\r\n") {
		if strings.HasPrefix(line, name+":") {
			return true
		}
	}
	return false
}

// exportWithAttachment exports a one-attachment message carrying the given
// filename and content type, both of which reach MIME part headers.
func exportWithAttachment(t *testing.T, filename, mimeType string) []byte {
	t.Helper()
	var props mapi.PropertyValues
	props.Set(mapi.PrSubject, "carrying an attachment")
	props.Set(mapi.PrBody, "see attached")
	props.Set(mapi.PrInternetMessageID, "<id@hermex.test>")

	var ap mapi.PropertyValues
	ap.Set(mapi.PrAttachMethod, int32(mapi.AttachByValue))
	ap.Set(mapi.PrAttachDataBin, []byte("payload"))
	ap.Set(mapi.PrAttachLongFilename, filename)
	ap.Set(mapi.PrAttachMimeTag, mimeType)

	msg := &Message{Props: props, Attachments: []Attachment{{Props: ap}}}
	out, err := Export(msg, Options{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return out
}

// TestAttachmentFilenameCannotInjectHeaders is the MIME injection defect. The
// filename and content type are client-written on every protocol and go straight
// into part header lines, and the multipart writer emits header values verbatim
// without validating them. A line break therefore started a header of the sender's
// choosing, and a blank line ended the part header block and pushed the rest into
// the part body of mail relayed to external recipients.
func TestAttachmentFilenameCannotInjectHeaders(t *testing.T) {
	out := exportWithAttachment(t, "report.pdf\"\r\nX-Injected: yes\r\n\r\nsmuggled", "application/pdf")

	if injectedHeaderLine(out, "X-Injected") {
		t.Errorf("a filename line break injected a header:\n%s", out)
	}
	// The smuggled text must remain inside the parameter it was written into,
	// never on a line of its own after a blank line.
	for line := range strings.SplitSeq(string(out), "\r\n") {
		if line == "smuggled" {
			t.Errorf("a filename injected part body content:\n%s", out)
		}
	}
}

// TestAttachmentMimeTypeCannotInjectHeaders covers the other client-written value
// on the same header line.
func TestAttachmentMimeTypeCannotInjectHeaders(t *testing.T) {
	out := exportWithAttachment(t, "report.pdf", "application/pdf\r\nX-Injected: yes")

	if injectedHeaderLine(out, "X-Injected") {
		t.Errorf("a content type line break injected a header:\n%s", out)
	}
}

// TestOrdinaryAttachmentHeadersSurvive is the control: sanitizing must not damage
// an ordinary filename, which still has to reach the recipient intact.
func TestOrdinaryAttachmentHeadersSurvive(t *testing.T) {
	out := exportWithAttachment(t, "quarterly report.pdf", "application/pdf")

	if !strings.Contains(string(out), "quarterly report.pdf") {
		t.Errorf("an ordinary filename did not survive export:\n%s", out)
	}
	if !strings.Contains(string(out), "application/pdf") {
		t.Errorf("an ordinary content type did not survive export:\n%s", out)
	}
}

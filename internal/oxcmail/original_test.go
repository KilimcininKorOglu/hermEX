package oxcmail

import (
	"strings"
	"testing"
	"unicode/utf8"

	"hermex/internal/mapi"
)

// The expectations come from the chosen smart-response behavior: the original
// follows the client's text as a header block and its body, inside the one HTML
// document when the reply has HTML, and a forward (only) carries its attachments.

func originalMessage() *Message {
	var p mapi.PropertyValues
	p.Set(mapi.PrSentRepresentingName, "Bob")
	p.Set(mapi.PrSentRepresentingEmailAddress, "bob@hermex.test")
	p.Set(mapi.PrDisplayTo, "Alice")
	p.Set(mapi.PrSubject, "<script>alert(1)</script> plan")
	p.Set(mapi.PrBody, "original text")
	p.Set(mapi.PrHTML, []byte("<html><body><p>original <b>html</b></p></body></html>"))
	p.Set(mapi.PrInternetCodepage, int32(cpUTF8))
	var a mapi.PropertyValues
	a.Set(mapi.PrAttachLongFilename, "plan.pdf")
	return &Message{Props: p, Attachments: []Attachment{{Props: a}}}
}

func TestAppendOriginalPlainReply(t *testing.T) {
	var p mapi.PropertyValues
	p.Set(mapi.PrBody, "my answer")
	reply := &Message{Props: p}
	AppendOriginal(reply, originalMessage(), false)

	body := propString(reply.Props, mapi.PrBody)
	for _, want := range []string{"my answer", originalSeparator, "From: Bob <bob@hermex.test>", "To: Alice", "original text"} {
		if !strings.Contains(body, want) {
			t.Errorf("plain body lacks %q:\n%s", want, body)
		}
	}
	if strings.Index(body, "my answer") > strings.Index(body, originalSeparator) {
		t.Error("the original comes before the client's text")
	}
	if len(reply.Attachments) != 0 {
		t.Errorf("a reply carries %d of the original's attachments, want 0", len(reply.Attachments))
	}
}

func TestAppendOriginalForwardCarriesAttachments(t *testing.T) {
	var p mapi.PropertyValues
	p.Set(mapi.PrBody, "fyi")
	fwd := &Message{Props: p}
	AppendOriginal(fwd, originalMessage(), true)
	if len(fwd.Attachments) != 1 || propString(fwd.Attachments[0].Props, mapi.PrAttachLongFilename) != "plan.pdf" {
		t.Errorf("forward attachments = %v, want the original's plan.pdf", fwd.Attachments)
	}
}

// TestAppendOriginalHTMLStaysOneDocument checks the block lands inside the
// reply's body element, the original's markup is inlined without its own html or
// body element, and a header value is escaped rather than rendered as markup.
func TestAppendOriginalHTMLStaysOneDocument(t *testing.T) {
	var p mapi.PropertyValues
	p.Set(mapi.PrBody, "my answer")
	p.Set(mapi.PrHTML, []byte("<html><body><p>my answer</p></body></html>"))
	p.Set(mapi.PrInternetCodepage, int32(cpUTF8))
	reply := &Message{Props: p}
	AppendOriginal(reply, originalMessage(), false)

	raw, _ := bytesProp(reply.Props, mapi.PrHTML)
	doc := string(raw)
	if n := strings.Count(strings.ToLower(doc), "<body"); n != 1 {
		t.Errorf("result holds %d body elements, want 1:\n%s", n, doc)
	}
	if !strings.HasSuffix(doc, "</body></html>") || !strings.Contains(doc, "original <b>html</b>") {
		t.Errorf("original not inlined inside the reply's body:\n%s", doc)
	}
	if strings.Contains(doc, "<script>") || !strings.Contains(doc, "&lt;script&gt;") {
		t.Errorf("subject markup is not escaped:\n%s", doc)
	}
}

// TestAppendOriginalTranscodesTheReplyHTML gives a reply whose HTML is in
// ISO-8859-9: the merged document must be UTF-8 and say so, or the reply's own
// Turkish letters turn to mojibake next to the UTF-8 original.
func TestAppendOriginalTranscodesTheReplyHTML(t *testing.T) {
	var p mapi.PropertyValues
	p.Set(mapi.PrHTML, []byte("<p>te\xfeekk\xfcrler</p>")) // "teşekkürler" in ISO-8859-9
	p.Set(mapi.PrInternetCodepage, int32(28599))
	reply := &Message{Props: p}
	AppendOriginal(reply, originalMessage(), false)

	raw, _ := bytesProp(reply.Props, mapi.PrHTML)
	if !utf8.Valid(raw) || !strings.Contains(string(raw), "teşekkürler") {
		t.Errorf("merged HTML is not the UTF-8 reply: %q", raw)
	}
	if cp, _ := reply.Props.Get(mapi.PrInternetCodepage); cp != int32(cpUTF8) {
		t.Errorf("codepage = %v, want %d", cp, cpUTF8)
	}
}

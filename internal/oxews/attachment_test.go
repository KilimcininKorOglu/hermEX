package oxews

import (
	"encoding/xml"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// embeddedAtt is a method-5 attachment carrying an encapsulated message.
func embeddedAtt() oxcmail.Attachment {
	return oxcmail.Attachment{Props: mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachEmbeddedMsg)},
		{Tag: mapi.PrAttachMimeTag, Value: "message/rfc822"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("From: orig@hermex.test\r\nTo: rcpt@hermex.test\r\n" +
			"Subject: Inner Subject\r\n\r\nInner body line.\r\n")},
	}}
}

// fileAtt is an ordinary by-value file attachment.
func fileAtt() oxcmail.Attachment {
	return oxcmail.Attachment{Props: mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachLongFilename, Value: "f.txt"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("filedata")},
	}}
}

// TestEmbeddedAttachmentRoutedAsItemAttachment proves an embedded message is
// emitted as an ItemAttachment (not a FileAttachment): the metadata list routes
// it to Items, GetAttachment fills the nested Message item (subject + body from
// the encapsulated message), and the marshaled wire form is <ItemAttachment> with
// a nested <Message>.
func TestEmbeddedAttachmentRoutedAsItemAttachment(t *testing.T) {
	// Metadata list: embedded -> Items, file -> Files.
	list := BuildAttachments(13, 42, []oxcmail.Attachment{embeddedAtt(), fileAtt()}, "")
	if list == nil {
		t.Fatal("BuildAttachments returned nil")
	}
	mustCount(t, len(list.Items), 1, "the ItemAttachments")
	wantEq(t, len(list.Files), 1, "the FileAttachments")
	wantEq(t, list.Items[0].ContentType, "message/rfc822", "the ItemAttachment ContentType")
	if list.Items[0].Message != nil {
		t.Error("metadata-list ItemAttachment must not carry the nested item")
	}

	// GetAttachment content: the nested Message item is filled.
	ia := BuildItemAttachmentContent(13, 42, 0, embeddedAtt(), "")
	if ia.Message == nil {
		t.Fatal("BuildItemAttachmentContent produced no nested message item")
	}
	wantEq(t, ia.Message.Subject, "Inner Subject", "the nested item subject")
	if ia.Message.Body == nil {
		t.Fatal("the nested item carries no body")
	}
	wantContains(t, ia.Message.Body.Content, "Inner body line.", "the nested item body")

	// Wire form: <ItemAttachment> with a nested <Message>, not a <FileAttachment>.
	out, err := xml.Marshal(struct {
		XMLName xml.Name `xml:"Attachments"`
		AttachmentList
	}{AttachmentList: AttachmentList{Items: []ItemAttachment{ia}}})
	mustNoErr(t, err, "marshal the attachment list")
	wantContains(t, string(out), "<ItemAttachment>", "the marshaled attachment")
	wantContains(t, string(out), "Inner Subject", "the marshaled nested item")
}

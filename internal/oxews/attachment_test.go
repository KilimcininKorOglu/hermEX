package oxews

import (
	"encoding/xml"
	"strings"
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
	if len(list.Items) != 1 {
		t.Fatalf("ItemAttachments = %d, want 1", len(list.Items))
	}
	if len(list.Files) != 1 {
		t.Errorf("FileAttachments = %d, want 1", len(list.Files))
	}
	if ct := list.Items[0].ContentType; ct != "message/rfc822" {
		t.Errorf("ItemAttachment ContentType = %q, want message/rfc822", ct)
	}
	if list.Items[0].Message != nil {
		t.Error("metadata-list ItemAttachment must not carry the nested item")
	}

	// GetAttachment content: the nested Message item is filled.
	ia := BuildItemAttachmentContent(13, 42, 0, embeddedAtt(), "")
	if ia.Message == nil {
		t.Fatal("BuildItemAttachmentContent produced no nested message item")
	}
	if ia.Message.Subject != "Inner Subject" {
		t.Errorf("nested item subject = %q, want \"Inner Subject\"", ia.Message.Subject)
	}
	if ia.Message.Body == nil || !strings.Contains(ia.Message.Body.Content, "Inner body line.") {
		t.Errorf("nested item body = %+v, want it to contain \"Inner body line.\"", ia.Message.Body)
	}

	// Wire form: <ItemAttachment> with a nested <Message>, not a <FileAttachment>.
	out, err := xml.Marshal(struct {
		XMLName xml.Name `xml:"Attachments"`
		AttachmentList
	}{AttachmentList: AttachmentList{Items: []ItemAttachment{ia}}})
	if err != nil {
		t.Fatal(err)
	}
	xmlStr := string(out)
	if !strings.Contains(xmlStr, "<ItemAttachment>") {
		t.Errorf("marshaled attachment is not an ItemAttachment: %s", xmlStr)
	}
	if !strings.Contains(xmlStr, "Inner Subject") {
		t.Errorf("nested item subject missing from wire form: %s", xmlStr)
	}
}

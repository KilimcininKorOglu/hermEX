package oxews

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// AttachmentList is the EWS <t:Attachments> element.
type AttachmentList struct {
	Files []FileAttachment `xml:"FileAttachment"`
}

// FileAttachment is the EWS <t:FileAttachment> element. Content is filled only
// by GetAttachment; an item's attachment list carries metadata only.
type FileAttachment struct {
	AttachmentID AttachmentIDElem `xml:"AttachmentId"`
	Name         string           `xml:"Name,omitempty"`
	ContentType  string           `xml:"ContentType,omitempty"`
	ContentID    string           `xml:"ContentId,omitempty"`
	Size         int              `xml:"Size,omitempty"`
	Content      string           `xml:"Content,omitempty"`
}

// AttachmentIDElem is the EWS <t:AttachmentId> element.
type AttachmentIDElem struct {
	ID string `xml:"Id,attr"`
}

// EncodeAttachmentID encodes (message id, attachment index) as an opaque token.
// The index is the attachment's position in the message's attachment_id order,
// which OpenMessage returns stably.
func EncodeAttachmentID(messageID int64, index int) string {
	return base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d.%d", messageID, index))
}

// DecodeAttachmentID reverses EncodeAttachmentID.
func DecodeAttachmentID(s string) (messageID int64, index int, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, 0, errBadID
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 2 {
		return 0, 0, errBadID
	}
	mid, e1 := strconv.ParseInt(parts[0], 10, 64)
	idx, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil {
		return 0, 0, errBadID
	}
	return mid, idx, nil
}

// BuildAttachments builds the metadata-only attachment list for an item, or nil
// when there are none.
func BuildAttachments(messageID int64, atts []oxcmail.Attachment) *AttachmentList {
	if len(atts) == 0 {
		return nil
	}
	list := &AttachmentList{}
	for i, att := range atts {
		list.Files = append(list.Files, attachmentMeta(messageID, i, att))
	}
	return list
}

// BuildAttachmentContent builds the full FileAttachment (with base64 content)
// for GetAttachment.
func BuildAttachmentContent(messageID int64, index int, att oxcmail.Attachment) FileAttachment {
	fa := attachmentMeta(messageID, index, att)
	fa.Content = base64.StdEncoding.EncodeToString(binProp(att.Props, mapi.PrAttachDataBin))
	return fa
}

// attachmentMeta builds the metadata-only FileAttachment (no Content).
func attachmentMeta(messageID int64, index int, att oxcmail.Attachment) FileAttachment {
	return FileAttachment{
		AttachmentID: AttachmentIDElem{ID: EncodeAttachmentID(messageID, index)},
		Name:         attachName(att.Props),
		ContentType:  stringProp(att.Props, mapi.PrAttachMimeTag),
		ContentID:    stringProp(att.Props, mapi.PrAttachContentID),
		Size:         len(binProp(att.Props, mapi.PrAttachDataBin)),
	}
}

// attachName returns the attachment's filename, preferring the long form.
func attachName(props mapi.PropertyValues) string {
	if n := stringProp(props, mapi.PrAttachLongFilename); n != "" {
		return n
	}
	return stringProp(props, mapi.PrAttachFilename)
}

// binProp reads a PtBinary property as bytes.
func binProp(props mapi.PropertyValues, tag mapi.PropTag) []byte {
	if v, ok := props.Get(tag); ok {
		switch b := v.(type) {
		case []byte:
			return b
		case string:
			return []byte(b)
		}
	}
	return nil
}

package oxews

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// AttachmentList is the EWS <t:Attachments> element: a choice of file and item
// attachments. An embedded message (PR_ATTACH_METHOD = afEmbeddedMessage) is an
// item attachment; every other attachment is a file attachment.
type AttachmentList struct {
	Files []FileAttachment `xml:"FileAttachment"`
	Items []ItemAttachment `xml:"ItemAttachment"`
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

// ItemAttachment is the EWS <t:ItemAttachment> element — an attached item such as
// a forwarded message (an embedded message/rfc822 part). The nested Message item
// is filled only by GetAttachment; an item's attachment list carries metadata
// only. v1 emits the nested item as a Message (the only item type embedded
// attachments carry in this build).
type ItemAttachment struct {
	AttachmentID AttachmentIDElem `xml:"AttachmentId"`
	Name         string           `xml:"Name,omitempty"`
	ContentType  string           `xml:"ContentType,omitempty"`
	Size         int              `xml:"Size,omitempty"`
	Message      *Message         `xml:"Message,omitempty"`
}

// IsEmbeddedAttachment reports whether an attachment is an embedded message
// (afEmbeddedMessage), which EWS exposes as an ItemAttachment rather than a
// FileAttachment.
func IsEmbeddedAttachment(att oxcmail.Attachment) bool {
	return longProp(att.Props, mapi.PrAttachMethod, 0) == int32(mapi.AttachEmbeddedMsg)
}

// AttachmentIDElem is the EWS <t:AttachmentId> element.
type AttachmentIDElem struct {
	ID string `xml:"Id,attr"`
}

// EncodeAttachmentID encodes (folder id, message id, attachment index) as an opaque
// token, tagged with the target mailbox SMTP when the message lives in another mailbox
// the caller was granted access to. The folder id lets GetAttachment enforce read
// access on the parent folder; the mailbox routes it to the right store. The index is
// the attachment's position in the message's attachment_id order, which OpenMessage
// returns stably. An "|" precedes the mailbox (an SMTP address contains dots). A token
// with no mailbox segment decodes to an own-mailbox id; the legacy two-field form
// (message id, index) still decodes, with an unknown folder.
func EncodeAttachmentID(folderID, messageID int64, index int, mailbox string) string {
	s := fmt.Sprintf("%d.%d.%d", folderID, messageID, index)
	if mailbox != "" {
		s += "|" + mailbox
	}
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// DecodeAttachmentID reverses EncodeAttachmentID. A legacy two-field token decodes with
// folderID=0 (folder unknown) and an empty mailbox.
func DecodeAttachmentID(s string) (folderID, messageID int64, index int, mailbox string, err error) {
	raw, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil {
		return 0, 0, 0, "", errBadID
	}
	str := string(raw)
	if i := strings.IndexByte(str, '|'); i >= 0 {
		mailbox = str[i+1:]
		str = str[:i]
	}
	parts := strings.Split(str, ".")
	switch len(parts) {
	case 2: // legacy: message id, index (own mailbox, folder unknown)
		mid, e1 := strconv.ParseInt(parts[0], 10, 64)
		idx, e2 := strconv.Atoi(parts[1])
		if e1 != nil || e2 != nil {
			return 0, 0, 0, "", errBadID
		}
		return 0, mid, idx, mailbox, nil
	case 3:
		fid, e1 := strconv.ParseInt(parts[0], 10, 64)
		mid, e2 := strconv.ParseInt(parts[1], 10, 64)
		idx, e3 := strconv.Atoi(parts[2])
		if e1 != nil || e2 != nil || e3 != nil {
			return 0, 0, 0, "", errBadID
		}
		return fid, mid, idx, mailbox, nil
	default:
		return 0, 0, 0, "", errBadID
	}
}

// BuildAttachments builds the metadata-only attachment list for an item, or nil
// when there are none. An embedded message is listed as an ItemAttachment; every
// other attachment is a FileAttachment.
func BuildAttachments(folderID, messageID int64, atts []oxcmail.Attachment, mailbox string) *AttachmentList {
	if len(atts) == 0 {
		return nil
	}
	list := &AttachmentList{}
	for i, att := range atts {
		if IsEmbeddedAttachment(att) {
			list.Items = append(list.Items, itemAttachmentMeta(folderID, messageID, i, att, mailbox))
		} else {
			list.Files = append(list.Files, attachmentMeta(folderID, messageID, i, att, mailbox))
		}
	}
	return list
}

// BuildItemAttachmentContent builds the full ItemAttachment (with the nested
// message item) for GetAttachment: the embedded message's raw bytes are parsed
// and rendered as a nested <t:Message>. The nested item's own attachments are
// listed as metadata only.
func BuildItemAttachmentContent(folderID, messageID int64, index int, att oxcmail.Attachment, mailbox string) ItemAttachment {
	ia := itemAttachmentMeta(folderID, messageID, index, att, mailbox)
	raw := binProp(att.Props, mapi.PrAttachDataBin)
	emb, err := oxcmail.Import(raw, oxcmail.Options{})
	if err != nil {
		return ia // metadata only — the encapsulated bytes did not parse
	}
	content, bodyType := embeddedBodyContent(emb)
	nested := BuildItem(emb, ItemMeta{
		ItemID:         EncodeAttachmentID(folderID, messageID, index, mailbox),
		FolderID:       folderID,
		MessageID:      messageID,
		Mailbox:        mailbox,
		Body:           content,
		BodyType:       bodyType,
		Size:           len(raw),
		HasAttachments: len(emb.Attachments) > 0,
	})
	ia.Message = &nested
	return ia
}

// embeddedBodyContent extracts the displayable body of an embedded message from
// its imported property bag, preferring HTML over plain text.
func embeddedBodyContent(emb *oxcmail.Message) (content, bodyType string) {
	if v, ok := emb.Props.Get(mapi.PrHTML); ok {
		if b, ok := v.([]byte); ok && len(b) > 0 {
			return string(b), "HTML"
		}
	}
	if s := stringProp(emb.Props, mapi.PrBody); s != "" {
		return s, "Text"
	}
	return "", "Text"
}

// itemAttachmentMeta builds the metadata-only ItemAttachment (no nested item).
func itemAttachmentMeta(folderID, messageID int64, index int, att oxcmail.Attachment, mailbox string) ItemAttachment {
	return ItemAttachment{
		AttachmentID: AttachmentIDElem{ID: EncodeAttachmentID(folderID, messageID, index, mailbox)},
		Name:         attachName(att.Props),
		ContentType:  stringProp(att.Props, mapi.PrAttachMimeTag),
		Size:         len(binProp(att.Props, mapi.PrAttachDataBin)),
	}
}

// BuildAttachmentContent builds the full FileAttachment (with base64 content)
// for GetAttachment.
func BuildAttachmentContent(folderID, messageID int64, index int, att oxcmail.Attachment, mailbox string) FileAttachment {
	fa := attachmentMeta(folderID, messageID, index, att, mailbox)
	fa.Content = base64.StdEncoding.EncodeToString(binProp(att.Props, mapi.PrAttachDataBin))
	return fa
}

// attachmentMeta builds the metadata-only FileAttachment (no Content).
func attachmentMeta(folderID, messageID int64, index int, att oxcmail.Attachment, mailbox string) FileAttachment {
	return FileAttachment{
		AttachmentID: AttachmentIDElem{ID: EncodeAttachmentID(folderID, messageID, index, mailbox)},
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

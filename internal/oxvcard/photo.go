package oxvcard

import (
	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// photoFilename is the file name MS-OXOCNTC 2.2.1.8 gives a contact's picture.
const photoFilename = "ContactPicture.jpg"

// PhotoAttachment returns the contact's picture: the attachment flagged with
// PrAttachmentContactPhoto, or, for a contact stored before the import flagged
// its photo, an attachment with no file name, which is how that import stored
// one. A named, unflagged attachment is a file, never the picture. ok is false
// when the contact has no picture.
func PhotoAttachment(msg *oxcmail.Message) (oxcmail.Attachment, bool) {
	for _, att := range msg.Attachments {
		if v, ok := att.Props.Get(mapi.PrAttachmentContactPhoto); ok && v == true {
			return att, true
		}
	}
	for _, att := range msg.Attachments {
		if !att.Props.Has(mapi.PrAttachFilename) && !att.Props.Has(mapi.PrAttachLongFilename) {
			return att, true
		}
	}
	return oxcmail.Attachment{}, false
}

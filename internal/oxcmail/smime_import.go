package oxcmail

import (
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/mime"
)

// S/MIME message classes a message object carries ([MS-OXOSMIME] 2.1.1 and 2.1.2).
const (
	classSMIME             = "IPM.Note.SMIME"
	classSMIMEMultipartSig = "IPM.Note.SMIME.MultipartSigned"
	smimeAttachmentName    = "smime.p7m"
)

// importSMIME reshapes a message whose top-level entity is S/MIME into the message
// object a MAPI client reads it as ([MS-OXOSMIME] 2.1): one attachment that holds
// the security content, under an S/MIME message class. Without it Outlook sees a
// signed message as plain mail with a stray signature file and cannot open an
// encrypted one.
//
// A clear-signed message keeps the body its first part promotes and trades every
// attachment for one that holds the whole multipart/signed entity, its
// Content-Type header included, so the signed bytes survive unchanged. An opaque
// message is already its one attachment; it gains the class, and its Content-Type
// is kept as the PS_INTERNET_HEADERS property Export writes it back from.
func importSMIME(root *mime.Part, msg *Message, stamp uint64, opt Options) error {
	switch {
	case root.Type == "multipart" && root.Subtype == "signed":
		msg.Props.Set(mapi.PrMessageClass, classSMIMEMultipartSig)
		msg.Attachments = []Attachment{signedEntityAttachment(root, stamp)}
	case root.Type == "application" && (root.Subtype == "pkcs7-mime" || root.Subtype == "x-pkcs7-mime"):
		msg.Props.Set(mapi.PrMessageClass, classSMIME)
		if root.Filename() == "" && len(msg.Attachments) == 1 {
			msg.Attachments[0].Props.Set(mapi.PrAttachLongFilename, smimeAttachmentName)
			msg.Attachments[0].Props.Set(mapi.PrAttachExtension, ".p7m")
		}
		return keepContentType(msg, root, opt)
	}
	return nil
}

// signedEntityAttachment builds the one attachment of a clear-signed message object: the
// outer multipart/signed entity with its Content-Type header and no other header
// field ([MS-OXOSMIME] 2.1.1).
func signedEntityAttachment(root *mime.Part, stamp uint64) Attachment {
	ct := strings.TrimSpace(root.Header().Get("Content-Type"))
	data := make([]byte, 0, len(ct)+len(root.RawBody())+18)
	data = append(data, "Content-Type: "...)
	data = append(data, ct...)
	data = append(data, "\r\n\r\n"...)
	data = append(data, root.RawBody()...)
	var a Attachment
	a.Props.Set(mapi.PrAttachMethod, int32(mapi.AttachByValue))
	a.Props.Set(mapi.PrAttachMimeTag, "multipart/signed")
	a.Props.Set(mapi.PrAttachLongFilename, smimeAttachmentName)
	a.Props.Set(mapi.PrAttachExtension, ".p7m")
	a.Props.Set(mapi.PrCreationTime, stamp)
	a.Props.Set(mapi.PrLastModificationTime, stamp)
	a.Props.Set(mapi.PrAttachDataBin, data)
	return a
}

// keepContentType stores an opaque message's Content-Type, parameters and all, in
// the PS_INTERNET_HEADERS "Content-Type" named property. The smime-type parameter
// lives only there, and a client needs it to tell signed data from enveloped data.
func keepContentType(msg *Message, root *mime.Part, opt Options) error {
	ct := strings.TrimSpace(root.Header().Get("Content-Type"))
	if opt.Resolver == nil || ct == "" {
		return nil
	}
	ids, err := opt.Resolver(true, []mapi.PropertyName{mapi.NameInternetContentType})
	if err != nil {
		return err
	}
	if len(ids) == 1 && ids[0] != 0 {
		msg.Props.Set(mapi.MakeTag(ids[0], mapi.PtUnicode), ct)
	}
	return nil
}

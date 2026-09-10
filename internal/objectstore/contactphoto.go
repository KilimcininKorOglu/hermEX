package objectstore

import (
	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// A contact's picture is a hidden attachment on the IPM.Contact item, marked with
// PrAttachmentContactPhoto. It is the picture every surface shows for an address
// that is in the user's own address book but not in the organization's: webmail
// serves it directly, EWS serves it as the address's photo, and ActiveSync serves
// it as a resolved recipient's picture.

// ContactPhotoAttachment returns the attach number and image bytes of a contact's
// photo attachment. ok is false when the contact carries no photo.
func ContactPhotoAttachment(msg *oxcmail.Message) (attachNum int32, data []byte, ok bool) {
	for _, att := range msg.Attachments {
		if !photoMarked(att.Props) {
			continue
		}
		raw, hasData := attachBytes(att.Props)
		num, hasNum := attachNumber(att.Props)
		if hasData && hasNum {
			return num, raw, true
		}
	}
	return 0, nil, false
}

// photoMarked reports whether an attachment's property bag marks it as the
// contact's photo.
func photoMarked(props mapi.PropertyValues) bool {
	v, has := props.Get(mapi.PrAttachmentContactPhoto)
	if !has {
		return false
	}
	b, isBool := v.(bool)
	return isBool && b
}

// attachBytes reads an attachment's binary content.
func attachBytes(props mapi.PropertyValues) ([]byte, bool) {
	v, has := props.Get(mapi.PrAttachDataBin)
	if !has {
		return nil, false
	}
	raw, isRaw := v.([]byte)
	return raw, isRaw
}

// attachNumber reads an attachment's number, the handle a later edit addresses it by.
func attachNumber(props mapi.PropertyValues) (int32, bool) {
	v, has := props.Get(mapi.PrAttachNum)
	if !has {
		return 0, false
	}
	num, isInt := v.(int32)
	return num, isInt
}

// ContactPhotoFor returns the photo of the contact carrying the given e-mail
// address, or nil when no contact carries that address or the contact has no
// photo. The store is the caller's own mailbox, so this exposes no other
// mailbox's data.
func (s *Store) ContactPhotoFor(address string) ([]byte, error) {
	id, ok, err := s.contactIDForAddress(address)
	if err != nil || !ok {
		return nil, err
	}
	msg, err := s.OpenMessage(id)
	if err != nil {
		return nil, err
	}
	_, data, has := ContactPhotoAttachment(msg)
	if !has {
		return nil, nil
	}
	return data, nil
}

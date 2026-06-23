package objectstore

import "hermex/internal/mapi"

// photoTag resolves the cross-protocol user-photo named property (the provider
// "photo" property) to its store property tag, allocating an id when create is
// set. ok is false when no id exists and create is false.
func (s *Store) photoTag(create bool) (mapi.PropTag, bool, error) {
	ids, err := s.GetNamedPropIDs(create, []mapi.PropertyName{mapi.NameUserPhoto})
	if err != nil {
		return 0, false, err
	}
	if len(ids) == 0 || ids[0] == 0 {
		return 0, false, nil
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtBinary)), true, nil
}

// UserPhoto returns the mailbox owner's portrait as raw image bytes, or nil when
// none is set. This is the single cross-protocol source the address book (NSPI),
// EWS, and webmail all read, served to Outlook as PR_EMS_AB_THUMBNAIL_PHOTO.
func (s *Store) UserPhoto() ([]byte, error) {
	tag, ok, err := s.photoTag(false)
	if err != nil || !ok {
		return nil, err
	}
	props, err := s.GetStoreProperties(tag)
	if err != nil {
		return nil, err
	}
	if v, ok := props.Get(tag); ok {
		if b, ok := v.([]byte); ok && len(b) > 0 {
			return b, nil
		}
	}
	return nil, nil
}

// SetUserPhoto stores the mailbox owner's portrait as raw image bytes; an empty
// slice clears it.
func (s *Store) SetUserPhoto(photo []byte) error {
	tag, ok, err := s.photoTag(len(photo) > 0)
	if err != nil {
		return err
	}
	if !ok {
		return nil // nothing stored and nothing to clear
	}
	return s.SetStoreProperties(mapi.PropertyValues{{Tag: tag, Value: photo}})
}

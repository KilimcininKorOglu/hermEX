package objectstore

import (
	"net/mail"
	"strings"

	"hermex/internal/mapi"
)

// contactEmailNames are the three contact e-mail slots (PidLidEmail{1,2,3}Address,
// PSETID_Address) ContactHasAddress matches a sender against.
var contactEmailNames = []mapi.PropertyName{
	mapi.NameEmail1Address,
	mapi.NameEmail2Address,
	mapi.NameEmail3Address,
}

// ContactHasAddress reports whether the mailbox's Contacts folder holds a contact
// carrying the given e-mail address in any of its three e-mail slots. It backs the
// out-of-office "known senders only" external audience: an external auto-reply is
// withheld unless the sender is a known contact.
//
// The match is case-insensitive on the bare addr-spec (display name and angle
// brackets dropped); a blank address never matches. Named-property ids are
// resolved without allocation (create=false), so a mailbox that has never stored a
// contact e-mail resolves no ids and reports false without scanning the folder.
func (s *Store) ContactHasAddress(address string) (bool, error) {
	want := normalizeContactAddress(address)
	if want == "" {
		return false, nil
	}
	ids, err := s.GetNamedPropIDs(false, contactEmailNames)
	if err != nil {
		return false, err
	}
	var tags []mapi.PropTag
	for _, id := range ids {
		if id != 0 {
			tags = append(tags, mapi.PropTag(uint32(id)<<16|uint32(mapi.PtUnicode)))
		}
	}
	if len(tags) == 0 {
		return false, nil // no contact e-mail named ids allocated → nothing to match
	}
	objs, err := s.ListFolderObjects(int64(mapi.PrivateFIDContacts))
	if err != nil {
		return false, err
	}
	for _, obj := range objs {
		pv, err := s.GetMessageProperties(obj.ID, tags...)
		if err != nil {
			continue
		}
		for _, tag := range tags {
			if v, ok := pv.Get(tag); ok {
				if str, ok := v.(string); ok && normalizeContactAddress(str) == want {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// normalizeContactAddress reduces an e-mail address to a case-insensitive bare
// addr-spec for contact matching, dropping any display name and angle brackets.
// The empty string and the null return-path "<>" both reduce to "".
func normalizeContactAddress(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "<>" {
		return ""
	}
	if a, err := mail.ParseAddress(s); err == nil {
		return strings.ToLower(a.Address)
	}
	return strings.ToLower(strings.Trim(s, "<>"))
}

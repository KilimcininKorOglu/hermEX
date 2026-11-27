package dav

import (
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// sharedCollection is one owner collection the caller may read, with the rights
// they hold on it.
type sharedCollection struct {
	owner  directory.SharedMailbox
	rights uint32
}

// readableShared returns the shared mailboxes whose fid collection the caller may
// read, the owners whose collections are shared with the caller (collection
// sharing, #117). The folder rights decide it, as they decide every request on the
// collection (access.go); a directory without shared-mailbox enumeration yields
// none, so sharing is simply inert.
func (s *Server) readableShared(user string, fid int64) ([]sharedCollection, error) {
	lister, ok := s.accounts.(directory.SharedMailboxLister)
	if !ok {
		return nil, nil
	}
	mboxes, err := lister.SharedMailboxes(user)
	if err != nil {
		return nil, err
	}
	var out []sharedCollection
	for _, m := range mboxes {
		if strings.EqualFold(m.Address, user) {
			continue
		}
		rights, ok := s.sharedRights(m.StorePath, fid, user)
		if ok && rights&mapi.FrightsReadAny != 0 {
			out = append(out, sharedCollection{owner: m, rights: rights})
		}
	}
	return out, nil
}

// sharedRights reads the caller's rights on fid in one owner's mailbox. A mailbox
// that cannot be opened or read shares nothing, so it is left out of the listing
// rather than failing the caller's own home set.
func (s *Server) sharedRights(storePath string, fid int64, user string) (uint32, bool) {
	ost, err := objectstore.OpenExisting(storePath)
	if err != nil {
		return 0, false
	}
	defer s.closeStore(ost)
	rights, err := ost.ResolvePermission(fid, permissionName(user))
	return rights, err == nil
}

// sharedCalendars builds the home-set responses for the owner Calendar collections
// the caller may read, each under the owner's principal href so a client can open
// it, and each reporting the privileges the caller holds there.
func (s *Server) sharedCalendars(user string) ([]msResponse, error) {
	return s.sharedResponses(user, int64(mapi.PrivateFIDCalendar), func(st *objectstore.Store, owner string) (msResponse, error) {
		return calCollectionResponse(st, owner, calendarName, owner+" calendar", int64(mapi.PrivateFIDCalendar))
	})
}

// sharedAddressBooks builds the home-set responses for the owner contacts
// collections the caller may read, each under the owner's principal href.
func (s *Server) sharedAddressBooks(user string) ([]msResponse, error) {
	return s.sharedResponses(user, int64(mapi.PrivateFIDContacts), func(st *objectstore.Store, owner string) (msResponse, error) {
		return cardCollectionResponse(st, owner, addressbookName, owner+" contacts", int64(mapi.PrivateFIDContacts))
	})
}

// sharedResponses builds one response per readable shared fid collection.
func (s *Server) sharedResponses(user string, fid int64, build func(*objectstore.Store, string) (msResponse, error)) ([]msResponse, error) {
	shared, err := s.readableShared(user, fid)
	if err != nil {
		return nil, err
	}
	var out []msResponse
	for _, c := range shared {
		ost, err := objectstore.OpenExisting(c.owner.StorePath)
		if err != nil {
			continue
		}
		cr, cerr := build(ost, c.owner.Address)
		s.closeStore(ost)
		if cerr != nil {
			continue
		}
		setPrivileges(&cr, c.rights)
		out = append(out, cr)
	}
	return out, nil
}

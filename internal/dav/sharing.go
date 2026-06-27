package dav

import (
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// delegatedMailboxes returns the shared mailboxes that list `user` as a delegate, i.e.
// the owners whose collections are shared with the caller (collection sharing, #117).
// It uses the directory's shared-mailbox enumeration; a directory without it (or with
// no shared mailboxes) yields none, so sharing is simply inert.
func (s *Server) delegatedMailboxes(user string) ([]directory.SharedMailbox, error) {
	lister, ok := s.accounts.(directory.SharedMailboxLister)
	if !ok {
		return nil, nil
	}
	mboxes, err := lister.SharedMailboxes()
	if err != nil {
		return nil, err
	}
	var out []directory.SharedMailbox
	for _, m := range mboxes {
		if strings.EqualFold(m.Address, user) {
			continue
		}
		ost, err := objectstore.Open(m.StorePath)
		if err != nil {
			continue
		}
		dels, derr := ost.GetDelegates()
		ost.Close()
		if derr != nil {
			continue
		}
		for _, d := range dels {
			if strings.EqualFold(d, user) {
				out = append(out, m)
				break
			}
		}
	}
	return out, nil
}

// sharedCalendars builds the home-set responses for the owner Calendar collections
// shared with the caller, each under the owner's principal href so a client can open
// it (access is gated at the routing layer).
func (s *Server) sharedCalendars(user string) ([]msResponse, error) {
	owners, err := s.delegatedMailboxes(user)
	if err != nil {
		return nil, err
	}
	var out []msResponse
	for _, m := range owners {
		ost, err := objectstore.Open(m.StorePath)
		if err != nil {
			continue
		}
		cr, cerr := calCollectionResponse(ost, m.Address, calendarName, m.Address+" calendar", int64(mapi.PrivateFIDCalendar))
		ost.Close()
		if cerr != nil {
			continue
		}
		out = append(out, cr)
	}
	return out, nil
}

// sharedAddressBooks builds the home-set responses for the owner contacts collections
// shared with the caller, each under the owner's principal href.
func (s *Server) sharedAddressBooks(user string) ([]msResponse, error) {
	owners, err := s.delegatedMailboxes(user)
	if err != nil {
		return nil, err
	}
	var out []msResponse
	for _, m := range owners {
		ost, err := objectstore.Open(m.StorePath)
		if err != nil {
			continue
		}
		cr, cerr := cardCollectionResponse(ost, m.Address, addressbookName, m.Address+" contacts", int64(mapi.PrivateFIDContacts))
		ost.Close()
		if cerr != nil {
			continue
		}
		out = append(out, cr)
	}
	return out, nil
}

package objectstore

import (
	"strings"

	"hermex/internal/mapi"
)

// DelegateFolders are the folders a delegate is given access through: the ones
// Outlook's delegate dialog and EWS AddDelegate set a level on. A grant made in
// any surface lands on these folders' permission tables, so every surface reads
// the same access back.
var DelegateFolders = []int64{
	mapi.PrivateFIDInbox,
	mapi.PrivateFIDCalendar,
	mapi.PrivateFIDContacts,
	mapi.PrivateFIDTasks,
	mapi.PrivateFIDNotes,
	mapi.PrivateFIDJournal,
}

// DelegateAccess is the access level every surface offers for a delegate's
// folders: none, read (Reviewer) or write (Editor).
type DelegateAccess string

const (
	DelegateNoAccess DelegateAccess = ""
	DelegateRead     DelegateAccess = "read"
	DelegateWrite    DelegateAccess = "write"
)

// Rights is the folder rights mask an access level sets on each delegate folder,
// normalized with the free/busy bits the levels imply. An unknown level is none.
func (a DelegateAccess) Rights() uint32 {
	switch a {
	case DelegateWrite:
		return mapi.NormalizeRights(mapi.RightsEditor, true)
	case DelegateRead:
		return mapi.NormalizeRights(mapi.RightsReviewer, true)
	}
	return 0
}

// DelegateAccessOf reads the access level a grantee holds back from the rights
// on the delegate folders, whichever surface granted them: write when any folder
// lets them delete every item, the right the shared-mailbox write gate checks,
// read when any lets them read every item.
func DelegateAccessOf(byFolder map[int64]uint32) DelegateAccess {
	var union uint32
	for _, r := range byFolder {
		union |= r
	}
	switch {
	case union&mapi.FrightsDeleteAny != 0:
		return DelegateWrite
	case union&mapi.FrightsReadAny != 0:
		return DelegateRead
	}
	return DelegateNoAccess
}

// SetDelegateFolderRights gives user the same rights on every delegate folder,
// replacing the grant each held for user. Zero rights remove the grant. The
// rights are stored as given; the caller normalizes them.
func (s *Store) SetDelegateFolderRights(user string, rights uint32) error {
	for _, fid := range DelegateFolders {
		if err := s.clearUserGrant(fid, user); err != nil {
			return err
		}
		if rights == 0 {
			continue
		}
		if err := s.ModifyPermissions(fid, false, []PermissionChange{{Op: PermAdd, Username: user, Rights: rights}}); err != nil {
			return err
		}
	}
	return nil
}

// DelegateFolderRights returns the rights user holds under their own name on
// each delegate folder that grants them any, matching the name without regard
// to case, as the address was written by whichever surface made the grant.
func (s *Store) DelegateFolderRights(user string) (map[int64]uint32, error) {
	out := map[int64]uint32{}
	for _, fid := range DelegateFolders {
		entries, err := s.ListPermissions(fid)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if strings.EqualFold(e.Name, user) && e.Rights != 0 {
				out[fid] = e.Rights
			}
		}
	}
	return out, nil
}

// clearUserGrant drops every row that names user on a folder, whatever the case
// of the stored name.
func (s *Store) clearUserGrant(fid int64, user string) error {
	_, err := s.objdb.Exec(`DELETE FROM permissions WHERE folder_id=? AND username=? COLLATE NOCASE`, fid, user)
	if err != nil {
		s.logStoreError("clear-user-grant", err)
	}
	return err
}

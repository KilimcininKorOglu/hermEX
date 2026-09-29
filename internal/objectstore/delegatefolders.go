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

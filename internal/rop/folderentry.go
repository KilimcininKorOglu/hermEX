package rop

import (
	"slices"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// eitLTPrivateFolder is the FolderType of a folder in a private mailbox
// ([MS-OXCDATA] 2.2.4).
const eitLTPrivateFolder uint16 = 0x0001

// PidTagAccess bits ([MS-OXCPRPT] 2.2.1.1).
const (
	accessModify          int32 = 0x01
	accessRead            int32 = 0x02
	accessDelete          int32 = 0x04
	accessCreateHierarchy int32 = 0x08
	accessCreateContents  int32 = 0x10
	accessCreateAssoc     int32 = 0x20
)

// rightsResolver reports the caller's effective rights on a folder.
type rightsResolver func(fid int64) (uint32, error)

// folderEntryID builds a folder's long-term entry id ([MS-OXCDATA] 2.2.4.1). In a
// private mailbox the provider uid is the MailboxGuid the logon reports, and the
// database guid is the GUID of the folder id's replica, which for hermEX's single
// replica is the same store GUID (see replidToGUID).
func folderEntryID(store *objectstore.Store, fid int64) ([]byte, error) {
	guid, err := store.StoreGUID()
	if err != nil {
		return nil, err
	}
	p := ext.NewPush(0)
	p.FolderEntryID(mapi.FolderEntryID{
		ProviderUID:  guid.Flat(),
		EIDType:      eitLTPrivateFolder,
		FolderDBGUID: guid,
		FolderGC:     mapi.ValueToGC(uint64(fid)), // #nosec G115 -- a folder id is a non-negative global counter
	})
	return p.Bytes(), nil
}

// folderIdentityTags are the folder properties derived from its ids and the
// caller's rights rather than read from the store.
var folderIdentityTags = []mapi.PropTag{mapi.PrEntryID, mapi.PrParentEntryID, mapi.PrRights, mapi.PrAccess}

// addFolderIdentity sets a folder's entry id and its parent's ([MS-OXCFOLD]
// 2.2.2.2.1.7), and, when the caller's rights are known, PidTagRights and
// PidTagAccess. Only the requested tags are set, or all of them for an empty
// request.
func addFolderIdentity(props *mapi.PropertyValues, store *objectstore.Store, fid int64, tags []mapi.PropTag, rights rightsResolver) error {
	want := func(t mapi.PropTag) bool { return len(tags) == 0 || slices.Contains(tags, t) }
	if want(mapi.PrEntryID) {
		eid, err := folderEntryID(store, fid)
		if err != nil {
			return err
		}
		props.Set(mapi.PrEntryID, eid)
	}
	if err := addParentEntryID(props, store, fid, want(mapi.PrParentEntryID)); err != nil {
		return err
	}
	if rights == nil || (!want(mapi.PrRights) && !want(mapi.PrAccess)) {
		return nil
	}
	r, err := rights(fid)
	if err != nil {
		return err
	}
	// PidTagRights has the PidTagMemberRights format without the free/busy bits
	// ([MS-OXCFOLD] 2.2.2.2.2.8).
	r &^= mapi.FrightsFreeBusySimple | mapi.FrightsFreeBusyDetailed
	props.Set(mapi.PrRights, int32(r)) // #nosec G115 -- the rights bits fit in 16
	props.Set(mapi.PrAccess, folderAccess(r))
	return nil
}

// addParentEntryID sets the entry id of the folder's parent, which a root lacks.
func addParentEntryID(props *mapi.PropertyValues, store *objectstore.Store, fid int64, wanted bool) error {
	if !wanted {
		return nil
	}
	computed, err := store.FolderComputedProps(fid)
	if err != nil {
		return err
	}
	v, ok := computed.Get(mapi.PrParentFolderID)
	if !ok {
		return nil
	}
	parent := int64(mapi.EID(uint64(v.(int64))).GCValue()) // #nosec G115 -- the EID round-trips its own bits
	eid, err := folderEntryID(store, parent)
	if err != nil {
		return err
	}
	props.Set(mapi.PrParentEntryID, eid)
	return nil
}

// folderAccess derives PidTagAccess from the caller's folder rights: what they may
// do to the folder object itself and which of its tables they may add to.
func folderAccess(r uint32) int32 {
	var a int32
	if r&mapi.FrightsVisible != 0 {
		a |= accessRead
	}
	if r&mapi.FrightsOwner != 0 {
		a |= accessModify | accessDelete | accessCreateAssoc
	}
	if r&mapi.FrightsCreateSubfolder != 0 {
		a |= accessCreateHierarchy
	}
	if r&mapi.FrightsCreate != 0 {
		a |= accessCreateContents
	}
	return a
}

// rightsFor returns the resolver of the caller's rights on a store's folders: the
// full set for the owner, the resolved grant for a delegate.
func (s *Session) rightsFor(store *objectstore.Store) rightsResolver {
	return func(fid int64) (uint32, error) {
		caller, delegate := s.delegateCallers[store]
		if !delegate {
			return mapi.RightsAll, nil
		}
		return store.ResolvePermission(fid, caller)
	}
}

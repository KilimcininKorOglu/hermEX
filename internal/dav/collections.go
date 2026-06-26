package dav

import (
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// Collection resolution (RFC 4791/6352 multi-collection). hermEX exposes the
// well-known Calendar and Contacts folders under the reserved names
// "calendar"/"contacts", plus any user-created calendar/contact subfolder addressed
// by its display name. A request path resolves to the folder id that parents that
// collection's objects.

func calCollectionFID(st *objectstore.Store, name string) (int64, bool, error) {
	return collectionFID(st, int64(mapi.PrivateFIDCalendar), calendarName, name)
}

func cardCollectionFID(st *objectstore.Store, name string) (int64, bool, error) {
	return collectionFID(st, int64(mapi.PrivateFIDContacts), addressbookName, name)
}

// collectionFID maps a collection URL segment to its folder id: the reserved default
// name is the well-known root folder; any other name is a child of that root matched
// by display name. ok is false when no such collection exists.
func collectionFID(st *objectstore.Store, root int64, defaultName, name string) (int64, bool, error) {
	if name == "" || name == defaultName {
		return root, true, nil
	}
	return st.FolderByName(&root, name)
}

// childCollections returns the child folders of a collection root — the additional
// calendars/address books the home-set PROPFIND advertises beyond the well-known one.
func childCollections(st *objectstore.Store, root int64) ([]objectstore.FolderInfo, error) {
	folders, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	var out []objectstore.FolderInfo
	for _, f := range folders {
		if f.ParentID != nil && *f.ParentID == root {
			out = append(out, f)
		}
	}
	return out, nil
}

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
	// The scheduling Inbox/Outbox share the calendar URL space but are not user
	// calendars: never resolve them as one (RFC 6638 §2.1/§2.2).
	if isReservedScheduleName(name) {
		return 0, false, nil
	}
	// The Tasks collection lives in the calendar URL space (served as VTODO) but is a
	// distinct well-known folder, not a child of the Calendar.
	if name == tasksName {
		return int64(mapi.PrivateFIDTasks), true, nil
	}
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
// calendars/address books the home-set PROPFIND advertises beyond the well-known
// one. Folders named for a reserved scheduling collection are skipped: they back the
// scheduling Inbox, not a user calendar, and are served under their own kind.
func childCollections(st *objectstore.Store, root int64) ([]objectstore.FolderInfo, error) {
	folders, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	var out []objectstore.FolderInfo
	for _, f := range folders {
		if f.ParentID != nil && *f.ParentID == root && !isReservedScheduleName(f.DisplayName) {
			out = append(out, f)
		}
	}
	return out, nil
}

// scheduleInboxFID resolves the per-mailbox scheduling Inbox folder — a reserved
// child of the Calendar root, addressed only through the scheduling-inbox kind and
// hidden from calendar enumeration. It is created on first use (create=true);
// otherwise ok is false when it does not yet exist.
func scheduleInboxFID(st *objectstore.Store, create bool) (int64, bool, error) {
	root := int64(mapi.PrivateFIDCalendar)
	fid, ok, err := st.FolderByName(&root, scheduleInboxName)
	if err != nil || ok {
		return fid, ok, err
	}
	if !create {
		return 0, false, nil
	}
	fid, err = st.CreateFolder(&root, scheduleInboxName)
	if err != nil {
		return 0, false, err
	}
	if err := st.SetFolderProperties(fid, mapi.PropertyValues{{Tag: mapi.PrContainerClass, Value: mapi.ContainerClassAppointment}}); err != nil {
		return 0, false, err
	}
	return fid, true, nil
}

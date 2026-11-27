package activesync

import (
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// easFolder is one folder advertised to the device: a stable collection id (the
// store folder id as a decimal string), its display name, and its EAS folder
// type.
type easFolder struct {
	id   string
	name string
	typ  int
}

// EAS folder types (MS-ASCMD 2.2.3.186.3 Type (FolderSync)).
const (
	folderTypeInbox        = 2
	folderTypeDrafts       = 3
	folderTypeDeleted      = 4
	folderTypeSent         = 5
	folderTypeOutbox       = 6
	folderTypeTask         = 7
	folderTypeCalendar     = 8
	folderTypeContact      = 9
	folderTypeNote         = 10
	folderTypeUserMail     = 12
	folderTypeUserCalendar = 13
	folderTypeUserContact  = 14
	folderTypeUserTask     = 15
	folderTypeUserJournal  = 16
	folderTypeUserNote     = 17
	folderSyncInvalidKey   = 9
)

// userFolderClasses pairs each user-created folder type (MS-ASCMD 12-17) with the
// container class the store keeps for it, so a folder made over EAS is the same
// kind of folder every other protocol reads, and a folder another protocol made
// reaches the device with its kind.
var userFolderClasses = []struct {
	typ   int
	class string
}{
	{folderTypeUserMail, mapi.ContainerClassNote},
	{folderTypeUserCalendar, mapi.ContainerClassAppointment},
	{folderTypeUserContact, mapi.ContainerClassContact},
	{folderTypeUserTask, mapi.ContainerClassTask},
	{folderTypeUserJournal, mapi.ContainerClassJournal},
	{folderTypeUserNote, mapi.ContainerClassStickyNote},
}

// userFolderTypeOf returns the EAS type of a user folder of the given class.
func userFolderTypeOf(class string) (int, bool) {
	if class == "" {
		return folderTypeUserMail, true
	}
	for _, u := range userFolderClasses {
		if u.class == class {
			return u.typ, true
		}
	}
	return 0, false
}

// userFolderClassOf returns the container class for a FolderCreate Type; the
// generic type 1 is a mail folder.
func userFolderClassOf(typ int) (string, bool) {
	if typ == 1 {
		return mapi.ContainerClassNote, true
	}
	for _, u := range userFolderClasses {
		if u.typ == typ {
			return u.class, true
		}
	}
	return "", false
}

// handleFolderSync answers FolderSync. SyncKey 0 primes the hierarchy and
// returns the mail folders with a fresh key; a matching key returns the same key
// with no changes; a stale key returns Status 9 so the device re-primes.
// FolderSync itself never sends incremental changes, a folder created through
// FolderCreate reaches the device in that command's own reply, and any later
// divergence is reconciled by a re-prime from key 0.
func (s *Server) handleFolderSync(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		s.failRequest(w, r, "wbxml.parse.fail", err, http.StatusBadRequest, "invalid WBXML")
		return
	}
	syncKey := root.ChildText(wbxml.FHSyncKey)

	st, err := objectstore.OpenExisting(sess.mailbox)
	if err != nil {
		s.failRequest(w, r, "foldersync.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}
	defer s.closeStore(sess.user, st)

	state, err := loadState(st)
	if err != nil {
		s.failRequest(w, r, "foldersync.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}
	dev := state.device(sess.req.deviceID)

	switch {
	case syncKey == "0":
		folders, err := syncFolders(st)
		if err != nil {
			s.failRequest(w, r, "foldersync.fail", err, http.StatusInternalServerError, "an internal error occurred")
			return
		}
		key := nextSyncKey(syncKey)
		if err := updateState(st, func(s *asState) bool {
			s.device(sess.req.deviceID).HierarchyKey = key
			return true
		}); err != nil {
			s.failRequest(w, r, "foldersync.fail", err, http.StatusInternalServerError, "an internal error occurred")
			return
		}
		writeWBXML(w, folderSyncResponse(key, folders))
	case syncKey == dev.HierarchyKey && syncKey != "":
		writeWBXML(w, folderSyncResponse(dev.HierarchyKey, nil))
	default:
		writeWBXML(w, folderSyncStatus(folderSyncInvalidKey))
	}
}

// syncFolders lists the folders to expose to the device: the mail folders plus the
// well-known Calendar, Contacts, Tasks, and Notes collections (every non-mail folder
// Sync serves).
func syncFolders(st *objectstore.Store) ([]easFolder, error) {
	list, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	var out []easFolder
	for _, f := range list {
		typ, ok, err := easFolderType(st, f.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, easFolder{id: strconv.FormatInt(f.ID, 10), name: f.DisplayName, typ: typ})
	}
	return out, nil
}

// wellKnownFolderTypes maps each built-in folder to the EAS type a device expects
// for it. A folder outside this table is classified by its container class.
var wellKnownFolderTypes = map[int64]int{
	mapi.PrivateFIDInbox:        folderTypeInbox,
	mapi.PrivateFIDDraft:        folderTypeDrafts,
	mapi.PrivateFIDDeletedItems: folderTypeDeleted,
	mapi.PrivateFIDSentItems:    folderTypeSent,
	mapi.PrivateFIDOutbox:       folderTypeOutbox,
	mapi.PrivateFIDCalendar:     folderTypeCalendar,
	mapi.PrivateFIDContacts:     folderTypeContact,
	mapi.PrivateFIDTasks:        folderTypeTask,
	mapi.PrivateFIDNotes:        folderTypeNote,
}

// easFolderType maps a store folder to its EAS type, reporting ok=false for a
// folder Sync cannot serve. The standard mail folders and the well-known Calendar,
// Contacts, Tasks, and Notes collections are mapped by their fixed ids; any other
// folder is a user-created folder typed by its container class (mail, calendar,
// contacts, tasks or notes), and a folder of any other class is left out.
func easFolderType(st *objectstore.Store, fid int64) (int, bool, error) {
	if typ, ok := wellKnownFolderTypes[fid]; ok {
		return typ, true, nil
	}
	class, err := folderClass(st, fid)
	if err != nil {
		return 0, false, err
	}
	typ, ok := userFolderTypeOf(class)
	return typ, ok, nil
}

// folderClass reads a folder's container class, "" when it has none.
func folderClass(st *objectstore.Store, fid int64) (string, error) {
	props, err := st.GetFolderProperties(fid, mapi.PrContainerClass)
	if err != nil {
		return "", err
	}
	class, _ := props.Get(mapi.PrContainerClass)
	cs, _ := class.(string)
	return cs, nil
}

// folderSyncResponse builds a Status-1 FolderSync reply carrying the new sync
// key and an Add for each folder.
func folderSyncResponse(key string, folders []easFolder) *wbxml.Node {
	changes := []*wbxml.Node{wbxml.Str(wbxml.FHCount, strconv.Itoa(len(folders)))}
	for _, f := range folders {
		changes = append(changes, wbxml.Elem(wbxml.FHAdd,
			wbxml.Str(wbxml.FHServerID, f.id),
			wbxml.Str(wbxml.FHParentID, "0"),
			wbxml.Str(wbxml.FHDisplayName, f.name),
			wbxml.Str(wbxml.FHType, strconv.Itoa(f.typ)),
		))
	}
	return wbxml.Elem(wbxml.FHFolderSync,
		wbxml.Str(wbxml.FHStatus, "1"),
		wbxml.Str(wbxml.FHSyncKey, key),
		wbxml.Elem(wbxml.FHChanges, changes...),
	)
}

// folderSyncStatus builds a bare FolderSync status reply (e.g. Status 9 to force
// a re-prime).
func folderSyncStatus(code int) *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderSync, wbxml.Str(wbxml.FHStatus, strconv.Itoa(code)))
}

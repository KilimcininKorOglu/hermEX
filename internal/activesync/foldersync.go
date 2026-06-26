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

// EAS folder types (MS-ASCMD FolderSync Type): the mail set v1 exposes.
const (
	folderTypeInbox      = 2
	folderTypeDrafts     = 3
	folderTypeDeleted    = 4
	folderTypeSent       = 5
	folderTypeOutbox     = 6
	folderTypeCalendar   = 8
	folderTypeContact    = 9
	folderTypeUserMail   = 12
	folderSyncInvalidKey = 9
)

// handleFolderSync answers FolderSync. SyncKey 0 primes the hierarchy and
// returns the mail folders with a fresh key; a matching key returns the same key
// with no changes; a stale key returns Status 9 so the device re-primes.
// FolderSync itself never sends incremental changes — a folder created through
// FolderCreate reaches the device in that command's own reply, and any later
// divergence is reconciled by a re-prime from key 0.
func (s *Server) handleFolderSync(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	syncKey := root.ChildText(wbxml.FHSyncKey)

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	state, err := loadState(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dev := state.device(sess.req.deviceID)

	switch {
	case syncKey == "0":
		folders, err := syncFolders(st)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		dev.HierarchyKey = nextSyncKey(syncKey)
		if err := saveState(st, state); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeWBXML(w, folderSyncResponse(dev.HierarchyKey, folders))
	case syncKey == dev.HierarchyKey && syncKey != "":
		writeWBXML(w, folderSyncResponse(dev.HierarchyKey, nil))
	default:
		writeWBXML(w, folderSyncStatus(folderSyncInvalidKey))
	}
}

// syncFolders lists the folders to expose to the device: the mail folders plus the
// well-known Calendar and Contacts collections (the non-mail folders Sync can serve).
// Other non-mail collections (tasks/notes) are skipped until Sync handles them.
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

// easFolderType maps a store folder to its EAS type, reporting ok=false for a
// folder Sync cannot serve. The standard mail folders and the well-known Calendar and
// Contacts collections are mapped by their fixed ids; any other folder is included as
// a generic mail folder only when its container class is a note (mail) folder.
func easFolderType(st *objectstore.Store, fid int64) (int, bool, error) {
	switch fid {
	case mapi.PrivateFIDInbox:
		return folderTypeInbox, true, nil
	case mapi.PrivateFIDDraft:
		return folderTypeDrafts, true, nil
	case mapi.PrivateFIDDeletedItems:
		return folderTypeDeleted, true, nil
	case mapi.PrivateFIDSentItems:
		return folderTypeSent, true, nil
	case mapi.PrivateFIDOutbox:
		return folderTypeOutbox, true, nil
	case mapi.PrivateFIDCalendar:
		return folderTypeCalendar, true, nil
	case mapi.PrivateFIDContacts:
		return folderTypeContact, true, nil
	}
	props, err := st.GetFolderProperties(fid, mapi.PrContainerClass)
	if err != nil {
		return 0, false, err
	}
	class, _ := props.Get(mapi.PrContainerClass)
	if cs, _ := class.(string); cs == "" || cs == mapi.ContainerClassNote {
		return folderTypeUserMail, true, nil
	}
	return 0, false, nil
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

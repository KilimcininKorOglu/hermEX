package activesync

import (
	"errors"
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// handleFolderUpdate answers FolderUpdate ([MS-ASCMD] 2.2.2.4): it renames a
// folder and/or re-parents it under the named ParentId, returning an advanced
// hierarchy sync key. The device's sync key must match the current hierarchy key
// (a mismatch reports Status 9 so the device re-primes). A built-in folder is
// protected (Status 3); a destination that already holds a sibling of the new
// name reports Status 2, and a missing parent reports Status 5. ParentId "0" is
// the mailbox root.
func (s *Server) handleFolderUpdate(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	syncKey := root.ChildText(wbxml.FHSyncKey)
	serverID := root.ChildText(wbxml.FHServerID)
	parentID := root.ChildText(wbxml.FHParentID)
	name := root.ChildText(wbxml.FHDisplayName)

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

	if serverID == "" || name == "" {
		writeWBXML(w, folderUpdateStatus(fhStatusBadRequest))
		return
	}
	if syncKey == "" || syncKey != dev.HierarchyKey {
		writeWBXML(w, folderUpdateStatus(fhStatusBadSyncKey))
		return
	}

	fid, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		writeWBXML(w, folderUpdateStatus(fhStatusNotFound))
		return
	}
	if fid < mapi.PrivateFIDUnassignedStart {
		writeWBXML(w, folderUpdateStatus(fhStatusSpecial))
		return
	}

	parent, code := resolveFolderParent(st, parentID)
	if code != 0 {
		writeWBXML(w, folderUpdateStatus(code))
		return
	}

	// Reject a name already taken by a different sibling at the destination so
	// name-based resolution stays unambiguous; renaming a folder to its own
	// current name (existing == fid) is a no-op rename, not a collision.
	if existing, exists, ferr := st.FolderByName(parent, name); ferr != nil {
		writeWBXML(w, folderUpdateStatus(fhStatusServerError))
		return
	} else if exists && existing != fid {
		writeWBXML(w, folderUpdateStatus(fhStatusExists))
		return
	}

	if err := st.RenameFolder(fid, parent, name); err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			writeWBXML(w, folderUpdateStatus(fhStatusNotFound))
			return
		}
		// A cycle (re-parenting into the folder's own subtree) has no dedicated
		// EAS status; report the generic server-error code.
		writeWBXML(w, folderUpdateStatus(fhStatusServerError))
		return
	}

	dev.HierarchyKey = nextSyncKey(dev.HierarchyKey)
	if err := saveState(st, state); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWBXML(w, folderUpdateResponse(dev.HierarchyKey))
}

// folderUpdateResponse builds a Status-1 FolderUpdate reply carrying the advanced
// hierarchy sync key.
func folderUpdateResponse(key string) *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderUpdate,
		wbxml.Str(wbxml.FHStatus, strconv.Itoa(fhStatusOK)),
		wbxml.Str(wbxml.FHSyncKey, key),
	)
}

// folderUpdateStatus builds a bare FolderUpdate status reply (e.g. Status 9 to
// force the device to re-prime its hierarchy).
func folderUpdateStatus(code int) *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderUpdate, wbxml.Str(wbxml.FHStatus, strconv.Itoa(code)))
}

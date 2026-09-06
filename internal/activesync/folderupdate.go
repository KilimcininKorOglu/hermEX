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
		s.failRequest(w, r, "wbxml.parse.fail", err, http.StatusBadRequest, "invalid WBXML")
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		s.failRequest(w, r, "folderupdate.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}
	defer st.Close()

	state, err := loadState(st)
	if err != nil {
		s.failRequest(w, r, "folderupdate.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}
	dev := state.device(sess.req.deviceID)

	if code := updateFolder(st, dev, root); code != fhStatusOK {
		writeWBXML(w, folderUpdateStatus(code))
		return
	}

	dev.HierarchyKey = nextSyncKey(dev.HierarchyKey)
	if err := saveState(st, state); err != nil {
		s.failRequest(w, r, "folderupdate.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}
	writeWBXML(w, folderUpdateResponse(dev.HierarchyKey))
}

// updateFolder validates a FolderUpdate request and performs the rename or
// re-parent it asks for, returning the EAS status to report.
func updateFolder(st *objectstore.Store, dev *deviceState, root *wbxml.Node) int {
	name := root.ChildText(wbxml.FHDisplayName)
	fid, code := folderUpdateTarget(dev, root, name)
	if code != fhStatusOK {
		return code
	}
	parent, code := folderUpdateParent(st, root.ChildText(wbxml.FHParentID), fid, name)
	if code != fhStatusOK {
		return code
	}
	return renameFolderStatus(st, fid, parent, name)
}

// folderUpdateTarget resolves the folder the request names, refusing an
// incomplete request, a stale hierarchy sync key, and a built-in folder.
func folderUpdateTarget(dev *deviceState, root *wbxml.Node, name string) (int64, int) {
	serverID := root.ChildText(wbxml.FHServerID)
	if serverID == "" || name == "" {
		return 0, fhStatusBadRequest
	}
	if syncKey := root.ChildText(wbxml.FHSyncKey); syncKey == "" || syncKey != dev.HierarchyKey {
		return 0, fhStatusBadSyncKey
	}
	fid, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return 0, fhStatusNotFound
	}
	if fid < mapi.PrivateFIDUnassignedStart {
		return 0, fhStatusSpecial
	}
	return fid, fhStatusOK
}

// folderUpdateParent resolves the destination parent and rejects a name already
// taken by a different sibling there, so name-based resolution stays unambiguous.
// Renaming a folder to its own current name (existing == fid) is a no-op rename,
// not a collision.
func folderUpdateParent(st *objectstore.Store, parentID string, fid int64, name string) (*int64, int) {
	parent, code := resolveFolderParent(st, parentID)
	if code != 0 {
		return nil, code
	}
	existing, exists, err := st.FolderByName(parent, name)
	if err != nil {
		return nil, fhStatusServerError
	}
	if exists && existing != fid {
		return nil, fhStatusExists
	}
	return parent, fhStatusOK
}

// renameFolderStatus performs the rename and maps its failure to an EAS status.
func renameFolderStatus(st *objectstore.Store, fid int64, parent *int64, name string) int {
	err := st.RenameFolder(fid, parent, name)
	switch {
	case err == nil:
		return fhStatusOK
	case errors.Is(err, objectstore.ErrNotFound):
		return fhStatusNotFound
	default:
		// A cycle (re-parenting into the folder's own subtree) has no dedicated
		// EAS status; report the generic server-error code.
		return fhStatusServerError
	}
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

package activesync

import (
	"errors"
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// handleFolderDelete answers FolderDelete ([MS-ASCMD] 2.2.2.3): it removes the
// named folder (and its subtree) and returns an advanced hierarchy sync key. The
// device's sync key must match the current hierarchy key; a mismatch reports
// Status 9 so the device re-primes. A built-in folder is protected, only ids at
// or above the unassigned range are deletable, matching the EWS and webmail
// folder-management guards, and reports Status 3.
func (s *Server) handleFolderDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		s.failRequest(w, r, "wbxml.parse.fail", err, http.StatusBadRequest, "invalid WBXML")
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		s.failRequest(w, r, "folder.delete.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}
	defer st.Close()

	state, err := loadState(st)
	if err != nil {
		s.failRequest(w, r, "folder.delete.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}
	dev := state.device(sess.req.deviceID)

	if code := deleteFolder(st, dev, root); code != fhStatusOK {
		writeWBXML(w, folderDeleteStatus(code))
		return
	}

	dev.HierarchyKey = nextSyncKey(dev.HierarchyKey)
	if err := saveState(st, state); err != nil {
		s.failRequest(w, r, "folder.delete.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}
	writeWBXML(w, folderDeleteResponse(dev.HierarchyKey))
}

// deleteFolder validates a FolderDelete request and removes the folder it names,
// returning the EAS status to report.
func deleteFolder(st *objectstore.Store, dev *deviceState, root *wbxml.Node) int {
	serverID := root.ChildText(wbxml.FHServerID)
	if serverID == "" {
		return fhStatusBadRequest
	}
	if syncKey := root.ChildText(wbxml.FHSyncKey); syncKey == "" || syncKey != dev.HierarchyKey {
		return fhStatusBadSyncKey
	}
	fid, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return fhStatusNotFound
	}
	if fid < mapi.PrivateFIDUnassignedStart {
		return fhStatusSpecial
	}
	switch err := st.DeleteFolder(fid); {
	case err == nil:
		return fhStatusOK
	case errors.Is(err, objectstore.ErrNotFound):
		return fhStatusNotFound
	default:
		return fhStatusServerError
	}
}

// folderDeleteResponse builds a Status-1 FolderDelete reply carrying the advanced
// hierarchy sync key.
func folderDeleteResponse(key string) *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderDelete,
		wbxml.Str(wbxml.FHStatus, strconv.Itoa(fhStatusOK)),
		wbxml.Str(wbxml.FHSyncKey, key),
	)
}

// folderDeleteStatus builds a bare FolderDelete status reply (e.g. Status 9 to
// force the device to re-prime its hierarchy).
func folderDeleteStatus(code int) *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderDelete, wbxml.Str(wbxml.FHStatus, strconv.Itoa(code)))
}

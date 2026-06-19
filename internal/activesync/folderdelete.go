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
// Status 9 so the device re-primes. A built-in folder is protected — only ids at
// or above the unassigned range are deletable, matching the EWS and webmail
// folder-management guards — and reports Status 3.
func (s *Server) handleFolderDelete(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	syncKey := root.ChildText(wbxml.FHSyncKey)
	serverID := root.ChildText(wbxml.FHServerID)

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

	if serverID == "" {
		writeWBXML(w, folderDeleteStatus(fhStatusBadRequest))
		return
	}
	if syncKey == "" || syncKey != dev.HierarchyKey {
		writeWBXML(w, folderDeleteStatus(fhStatusBadSyncKey))
		return
	}

	fid, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		writeWBXML(w, folderDeleteStatus(fhStatusNotFound))
		return
	}
	if fid < mapi.PrivateFIDUnassignedStart {
		writeWBXML(w, folderDeleteStatus(fhStatusSpecial))
		return
	}

	if err := st.DeleteFolder(fid); err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			writeWBXML(w, folderDeleteStatus(fhStatusNotFound))
			return
		}
		writeWBXML(w, folderDeleteStatus(fhStatusServerError))
		return
	}

	dev.HierarchyKey = nextSyncKey(dev.HierarchyKey)
	if err := saveState(st, state); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWBXML(w, folderDeleteResponse(dev.HierarchyKey))
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

package activesync

import (
	"net/http"
	"strconv"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// FolderHierarchy operation Status values (MS-ASCMD 2.2.3.167.1) shared by the
// FolderCreate/FolderDelete/FolderUpdate commands.
const (
	fhStatusOK             = 1
	fhStatusExists         = 2  // a folder with that name already exists at the parent
	fhStatusParentNotFound = 5  // the named parent folder does not exist
	fhStatusServerError    = 6  // an error occurred on the server
	fhStatusBadSyncKey     = 9  // the hierarchy sync key did not match — device must re-prime
	fhStatusBadRequest     = 10 // the request was malformed (e.g. an empty name)
)

// handleFolderCreate answers FolderCreate ([MS-ASCMD] 2.2.2.2): it creates a
// folder under the named parent and returns the new collection id together with
// an advanced hierarchy sync key. The device's sync key must match the current
// hierarchy key (the same key a FolderSync handed out); a mismatch reports
// Status 9 so the device re-primes from key 0. "0" names the mailbox root (a
// top-level folder); any other ParentId names an existing folder.
func (s *Server) handleFolderCreate(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	syncKey := root.ChildText(wbxml.FHSyncKey)
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

	if name == "" {
		writeWBXML(w, folderCreateStatus(fhStatusBadRequest))
		return
	}
	if syncKey == "" || syncKey != dev.HierarchyKey {
		writeWBXML(w, folderCreateStatus(fhStatusBadSyncKey))
		return
	}

	parent, ok := resolveFolderParent(w, st, parentID)
	if !ok {
		return
	}

	if _, exists, ferr := st.FolderByName(parent, name); ferr != nil {
		writeWBXML(w, folderCreateStatus(fhStatusServerError))
		return
	} else if exists {
		writeWBXML(w, folderCreateStatus(fhStatusExists))
		return
	}

	fid, err := st.CreateFolder(parent, name)
	if err != nil {
		writeWBXML(w, folderCreateStatus(fhStatusServerError))
		return
	}

	dev.HierarchyKey = nextSyncKey(dev.HierarchyKey)
	if err := saveState(st, state); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeWBXML(w, folderCreateResponse(dev.HierarchyKey, strconv.FormatInt(fid, 10)))
}

// resolveFolderParent maps an EAS ParentId to a store parent: "0" (or empty) is
// the mailbox root (nil), any other id must name an existing folder. On a missing
// or malformed parent it writes the status reply and reports ok=false.
func resolveFolderParent(w http.ResponseWriter, st *objectstore.Store, parentID string) (*int64, bool) {
	if parentID == "" || parentID == "0" {
		return nil, true
	}
	pid, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		writeWBXML(w, folderCreateStatus(fhStatusParentNotFound))
		return nil, false
	}
	exists, err := st.FolderExists(pid)
	if err != nil {
		writeWBXML(w, folderCreateStatus(fhStatusServerError))
		return nil, false
	}
	if !exists {
		writeWBXML(w, folderCreateStatus(fhStatusParentNotFound))
		return nil, false
	}
	return &pid, true
}

// folderCreateResponse builds a Status-1 FolderCreate reply carrying the advanced
// hierarchy sync key and the new folder's server id, in the wire order
// Status, SyncKey, ServerId.
func folderCreateResponse(key, serverID string) *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderCreate,
		wbxml.Str(wbxml.FHStatus, strconv.Itoa(fhStatusOK)),
		wbxml.Str(wbxml.FHSyncKey, key),
		wbxml.Str(wbxml.FHServerID, serverID),
	)
}

// folderCreateStatus builds a bare FolderCreate status reply (e.g. Status 9 to
// force the device to re-prime its hierarchy).
func folderCreateStatus(code int) *wbxml.Node {
	return wbxml.Elem(wbxml.FHFolderCreate, wbxml.Str(wbxml.FHStatus, strconv.Itoa(code)))
}

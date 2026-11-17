package webmail2api

import (
	"errors"
	"net/http"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// folderAccept reports whether a folder holds the kind of object an endpoint
// serves.
type folderAccept func(st *objectstore.Store, fid int64) bool

// inFolder accepts exactly one folder.
func inFolder(want int64) folderAccept {
	return func(_ *objectstore.Store, fid int64) bool { return fid == want }
}

// inCalendar accepts the default calendar and every calendar folder the user
// created, the folders the event list reads.
func inCalendar(st *objectstore.Store, fid int64) bool {
	if fid == mapi.PrivateFIDCalendar {
		return true
	}
	props, err := st.GetFolderProperties(fid, mapi.PrContainerClass)
	return err == nil && strings.EqualFold(propStr(props, mapi.PrContainerClass), mapi.ContainerClassAppointment)
}

// objectInKind answers 404 and returns false when the object a path names lives
// outside the folders its endpoint serves. The id comes from the client and the
// store addresses every object by it, so without this check a DELETE sent for an
// event could remove a task or a mail message. A missing object answers false
// too; the caller decides nothing else about it.
func objectInKind(w http.ResponseWriter, st *objectstore.Store, id int64, accept folderAccept) bool {
	fid, err := st.MessageFolder(id)
	if err != nil && !errors.Is(err, objectstore.ErrNotFound) {
		logError("object-folder", err, nil)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return false
	}
	if err != nil || !accept(st, fid) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return false
	}
	return true
}

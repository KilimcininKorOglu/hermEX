package dav

import (
	"net/http"
	"net/url"
	"strings"

	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/oxvcard"
)

// handleCopyMove implements COPY and MOVE for a calendar or address-book object
// (RFC 4918 §9.8/§9.9). The object is re-created in the destination collection
// through the same iCal/vCard path a PUT uses, so the copy gets a fresh identity
// rather than duplicating the source's, and for MOVE the source is then removed.
// Collection-level COPY/MOVE is not supported.
func (s *Server) handleCopyMove(w http.ResponseWriter, r *http.Request, mailbox string, move bool) {
	req, failure, status := parseCopyMove(r)
	if failure != "" {
		http.Error(w, failure, status)
		return
	}

	st, err := objectstore.Open(mailbox)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	defer st.Close()

	ends, failure, status, err := resolveCopyMove(st, r, req)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	if failure != "" {
		http.Error(w, failure, status)
		return
	}

	msg, err := reserializeObject(st, req, ends.src.ID)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.writeCopy(st, ends, msg, move); err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}

	if ends.dstExists {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// copyMoveRequest is one parsed COPY or MOVE: which protocol root it runs in and
// the source and destination it names.
type copyMoveRequest struct {
	isCal            bool
	ext              string
	srcColl, srcName string
	dstColl, dstName string
}

// copyMoveEnds are the resolved collections and objects the request names.
type copyMoveEnds struct {
	srcFID, dstFID int64
	src, dst       objectstore.FolderObject
	dstExists      bool
}

// parseCopyMove reads the request path and the Destination header. A non-empty
// failure is the message to answer with, at the given status.
func parseCopyMove(r *http.Request) (copyMoveRequest, string, int) {
	srcKind, _, srcColl, srcName := classify(r.URL.Path)
	isCal := strings.HasPrefix(r.URL.Path, "/dav/calendars/")
	isCard := strings.HasPrefix(r.URL.Path, "/dav/addressbooks/")
	req := copyMoveRequest{isCal: isCal, ext: ".vcf", srcColl: srcColl, srcName: srcName}
	wantKind := kindObject
	if isCal {
		wantKind, req.ext = kindCalObject, ".ics"
	}
	if (!isCal && !isCard) || srcKind != wantKind {
		return req, "COPY/MOVE is supported only on calendar/contact objects", http.StatusForbidden
	}

	dest := r.Header.Get("Destination")
	if dest == "" {
		return req, "missing Destination header", http.StatusBadRequest
	}
	destPath := dest
	if u, err := url.Parse(dest); err == nil && u.Path != "" {
		destPath = u.Path
	}
	dstKind, _, dstColl, dstName := classify(destPath)
	req.dstColl, req.dstName = dstColl, dstName
	// The destination must be the same kind of object under the same protocol root.
	if dstKind != wantKind || strings.HasPrefix(destPath, "/dav/calendars/") != isCal {
		return req, "destination must be the same kind of collection", http.StatusForbidden
	}
	return req, "", 0
}

// resolveCopyMove resolves both collections and both objects, and applies the
// preconditions RFC 4918 9.8 places on them.
func resolveCopyMove(st *objectstore.Store, r *http.Request, req copyMoveRequest) (copyMoveEnds, string, int, error) {
	var ends copyMoveEnds
	srcFID, dstFID, failure, status, err := copyMoveCollections(st, req)
	if err != nil || failure != "" {
		return ends, failure, status, err
	}
	ends.srcFID, ends.dstFID = srcFID, dstFID

	src, found, err := findObjectByName(st, srcFID, req.ext, req.srcName)
	if err != nil {
		return ends, "", 0, err
	}
	if !found {
		return ends, "source not found", http.StatusNotFound, nil
	}
	ends.src = src

	ends.dst, ends.dstExists, err = findObjectByName(st, dstFID, req.ext, req.dstName)
	if err != nil {
		return ends, "", 0, err
	}
	if ends.dstExists && r.Header.Get("Overwrite") == "F" {
		return ends, "destination exists", http.StatusPreconditionFailed, nil
	}
	return ends, "", 0, nil
}

// copyMoveCollections resolves both collections and applies the preconditions that
// concern them: the source must exist, the destination collection must already exist
// (RFC 4918 9.8.5: 409), and the two ends must not be the same resource.
func copyMoveCollections(st *objectstore.Store, req copyMoveRequest) (srcFID, dstFID int64, failure string, status int, err error) {
	srcFID, ok, err := collectionByKind(st, req.isCal, req.srcColl)
	if err != nil {
		return 0, 0, "", 0, err
	}
	if !ok {
		return 0, 0, "no such source collection", http.StatusNotFound, nil
	}
	dstFID, ok, err = collectionByKind(st, req.isCal, req.dstColl)
	if err != nil {
		return 0, 0, "", 0, err
	}
	if !ok {
		return 0, 0, "destination collection does not exist", http.StatusConflict, nil
	}
	if srcFID == dstFID && req.srcName == req.dstName {
		return 0, 0, "source and destination are the same resource", http.StatusForbidden, nil
	}
	return srcFID, dstFID, "", 0, nil
}

// reserializeObject re-imports the source through its own format, so the copy is
// a fresh message rather than a duplicate of the source's identity, and stamps it
// with the destination resource name.
func reserializeObject(st *objectstore.Store, req copyMoveRequest, srcID int64) (*oxcmail.Message, error) {
	tag, _, err := resourceNameTag(st, true)
	if err != nil {
		return nil, err
	}
	var msg *oxcmail.Message
	if req.isCal {
		data, err := calendarData(st, srcID)
		if err != nil {
			return nil, err
		}
		if msg, err = oxcical.Import([]byte(data), icalOptions(st)); err != nil {
			return nil, err
		}
	} else {
		data, err := addressData(st, srcID)
		if err != nil {
			return nil, err
		}
		if msg, err = oxvcard.Import([]byte(data), vcardOptions(st)); err != nil {
			return nil, err
		}
	}
	msg.Props.Set(tag, req.dstName)
	return msg, nil
}

// writeCopy replaces an existing destination with the new copy and, for a MOVE,
// routes the source through the dumpster so sync-collection reports a tombstone.
func (s *Server) writeCopy(st *objectstore.Store, ends copyMoveEnds, msg *oxcmail.Message, move bool) error {
	if ends.dstExists {
		if err := st.DeleteObject(ends.dst.ID); err != nil {
			return err
		}
	}
	if _, err := st.CreateMessage(ends.dstFID, msg); err != nil {
		return err
	}
	if move {
		return st.SoftDeleteObject(ends.src.ID)
	}
	return nil
}

// objectTarget describes the collection an object request must name, and the
// refusals to answer with when the path or the mailbox does not hold it.
type objectTarget struct {
	isCal     bool
	kind      resourceKind
	ext       string
	notObject string // 405 text for a path that names something else
	noColl    string // 404 text for a collection the mailbox does not hold
}

var (
	calTarget  = objectTarget{isCal: true, kind: kindCalObject, ext: ".ics", notObject: "not a calendar resource", noColl: "no such calendar"}
	cardTarget = objectTarget{isCal: false, kind: kindObject, ext: ".vcf", notObject: "not a contact resource", noColl: "no such address book"}
)

// openObjectCollection resolves the collection a DAV object request names and
// opens the mailbox store. The caller closes the store when ok is true; on a
// failure the response is already written. validateName is set by the handlers
// that accept a client-chosen resource name.
func (s *Server) openObjectCollection(w http.ResponseWriter, r *http.Request, mailbox string,
	t objectTarget, validateName bool) (*objectstore.Store, int64, string, bool) {
	kind, _, coll, name := classify(r.URL.Path)
	if kind != t.kind {
		http.Error(w, t.notObject, http.StatusMethodNotAllowed)
		return nil, 0, "", false
	}
	if validateName && !validObjectName(name) {
		http.Error(w, "invalid resource name", http.StatusBadRequest)
		return nil, 0, "", false
	}
	st, err := objectstore.Open(mailbox)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return nil, 0, "", false
	}
	fid, found, err := collectionByKind(st, t.isCal, coll)
	if err != nil {
		_ = st.Close()
		s.davError(w, err, http.StatusInternalServerError)
		return nil, 0, "", false
	}
	if !found {
		_ = st.Close()
		http.Error(w, t.noColl, http.StatusNotFound)
		return nil, 0, "", false
	}
	return st, fid, name, true
}

// collectionByKind resolves a collection name in the calendar or contacts root.
func collectionByKind(st *objectstore.Store, isCal bool, coll string) (int64, bool, error) {
	if isCal {
		return calCollectionFID(st, coll)
	}
	return cardCollectionFID(st, coll)
}

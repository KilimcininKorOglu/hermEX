package activesync

import (
	"errors"
	"net/http"
	"strconv"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// MoveItems status codes ([MS-ASCMD] 2.2.3.177.10). Note success is 3, not 1.
const (
	moveStatusInvalidSource = "1"
	moveStatusInvalidDest   = "2"
	moveStatusSuccess       = "3"
	moveStatusSameSrcDest   = "4"
	// moveStatusLocked is "source or destination item was locked", a transient
	// server condition the client retries: the status for a move the store
	// could not carry out. (Status 5 means more than one DstFldId.)
	moveStatusLocked = "7"
)

// handleMoveItems answers the MS-ASCMD MoveItems command, moving each item from
// its source folder to a destination folder. The destination server id is
// returned so the client can re-key the item.
func (s *Server) handleMoveItems(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		s.failRequest(w, r, "wbxml.parse.fail", err, http.StatusBadRequest, "invalid WBXML")
		return
	}
	st, err := objectstore.OpenExisting(sess.mailbox)
	if err != nil {
		s.failRequest(w, r, "moveitems.fail", err, http.StatusInternalServerError, "an internal error occurred")
		return
	}
	defer s.closeStore(sess.user, st)

	var responses []*wbxml.Node
	for _, mv := range root.Children {
		if mv.Tag == wbxml.MOMove {
			responses = append(responses, moveOne(st, mv))
		}
	}
	writeWBXML(w, wbxml.Elem(wbxml.MOMoves, responses...))
}

// moveRequest is one Move element of a MoveItems request.
type moveRequest struct {
	srcMsg, srcFld, dstFld string
	srcFolderID            int64
	dstFolderID            int64
}

// response renders the request's Response. The destination id echoes the
// source on any failure so the client keeps a valid id to retry.
func (m moveRequest) response(status, dstMsg string) *wbxml.Node {
	return wbxml.Elem(wbxml.MOResponse,
		wbxml.Str(wbxml.MOSrcMsgId, m.srcMsg),
		wbxml.Str(wbxml.MOStatus, status),
		wbxml.Str(wbxml.MODstMsgId, dstMsg))
}

// moveOne performs a single move and renders its Response. An item moves only
// between folders that hold its kind: mail between mail folders, and a calendar
// item, contact, task or note between folders of that class. A destination of
// another kind is refused with Status 2 and nothing moves, because no surface of
// that folder could read the item.
func moveOne(st *objectstore.Store, mv *wbxml.Node) *wbxml.Node {
	m := moveRequest{
		srcMsg: mv.ChildText(wbxml.MOSrcMsgId),
		srcFld: mv.ChildText(wbxml.MOSrcFldId),
		dstFld: mv.ChildText(wbxml.MODstFldId),
	}
	if status := m.resolveFolders(st); status != "" {
		return m.response(status, m.srcMsg)
	}
	srcCls, srcObj, err := objectClassFor(st, m.srcFolderID)
	if err != nil {
		st.LogSwallowedError("activesync.move-source-class", err)
		return m.response(moveStatusLocked, m.srcMsg)
	}
	dstCls, dstObj, err := objectClassFor(st, m.dstFolderID)
	if err != nil {
		st.LogSwallowedError("activesync.move-destination-class", err)
		return m.response(moveStatusLocked, m.srcMsg)
	}
	if srcObj != dstObj || srcCls.base != dstCls.base {
		return m.response(moveStatusInvalidDest, m.srcMsg)
	}
	if srcObj {
		return m.moveObject(st)
	}
	return m.moveMail(st)
}

// resolveFolders parses and checks both folder ids, answering the failure status
// or "" when both folders exist and differ.
func (m *moveRequest) resolveFolders(st *objectstore.Store) string {
	var err error
	if m.srcFolderID, err = strconv.ParseInt(m.srcFld, 10, 64); err != nil {
		return moveStatusInvalidSource
	}
	if m.dstFolderID, err = strconv.ParseInt(m.dstFld, 10, 64); err != nil {
		return moveStatusInvalidDest
	}
	if m.srcFolderID == m.dstFolderID {
		return moveStatusSameSrcDest
	}
	if status := folderStatus(st, m.srcFolderID, moveStatusInvalidSource, "activesync.move-source"); status != "" {
		return status
	}
	return folderStatus(st, m.dstFolderID, moveStatusInvalidDest, "activesync.move-destination")
}

// folderStatus answers missing when the folder does not exist, Status 7 when the
// store cannot say, and "" when it exists.
func folderStatus(st *objectstore.Store, fid int64, missing, stage string) string {
	exists, err := st.FolderExists(fid)
	if err != nil {
		st.LogSwallowedError(stage, err)
		return moveStatusLocked
	}
	if !exists {
		return missing
	}
	return ""
}

// moveMail moves an IMAP-indexed message, addressed by its per-folder UID; the
// destination id is the message's new UID there.
func (m moveRequest) moveMail(st *objectstore.Store) *wbxml.Node {
	uid, err := strconv.ParseUint(m.srcMsg, 10, 32)
	if err != nil {
		return m.response(moveStatusInvalidSource, m.srcMsg)
	}
	info, err := st.MoveMessage(m.srcFolderID, uint32(uid), m.dstFolderID)
	if err != nil {
		// The source no longer resolves (a stale client id) or the move failed.
		return m.response(moveStatusInvalidSource, m.srcMsg)
	}
	return m.response(moveStatusSuccess, strconv.FormatUint(uint64(info.UID), 10))
}

// moveObject moves an item the object store alone holds, addressed by its
// message id, which the move keeps, so every other protocol still names it the
// same. An id the source folder does not hold live (another folder's item, a
// soft-deleted one, a stale id) is Status 1 and nothing moves: a deleted item
// stays in the dumpster rather than being pulled out of it.
func (m moveRequest) moveObject(st *objectstore.Store) *wbxml.Node {
	id, err := strconv.ParseInt(m.srcMsg, 10, 64)
	if err != nil || id <= 0 {
		return m.response(moveStatusInvalidSource, m.srcMsg)
	}
	_, err = st.MoveMessageImport(m.srcFolderID, id, m.dstFolderID, id)
	if errors.Is(err, objectstore.ErrObjectDeleted) {
		return m.response(moveStatusInvalidSource, m.srcMsg)
	}
	if err != nil {
		st.LogSwallowedError("activesync.move-object", err)
		return m.response(moveStatusLocked, m.srcMsg)
	}
	return m.response(moveStatusSuccess, m.srcMsg)
}

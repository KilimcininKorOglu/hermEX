package activesync

import (
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
	// moveStatusFailed is "source or destination item was locked", a transient
	// server condition the client retries: the status for a move the store
	// could not carry out. (Status 5 means more than one DstFldId.)
	moveStatusFailed = "7"
)

// handleMoveItems answers the MS-ASCMD MoveItems command, moving each message from
// its source folder to a destination folder. The destination server id (the
// message's new per-folder UID) is returned so the client can re-key the item.
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
	defer st.Close()

	var responses []*wbxml.Node
	for _, mv := range root.Children {
		if mv.Tag == wbxml.MOMove {
			responses = append(responses, moveOne(st, mv))
		}
	}
	writeWBXML(w, wbxml.Elem(wbxml.MOMoves, responses...))
}

// moveOne performs a single move and renders its Response. The destination id
// echoes the source on any failure so the client keeps a valid id to retry.
func moveOne(st *objectstore.Store, mv *wbxml.Node) *wbxml.Node {
	srcMsg := mv.ChildText(wbxml.MOSrcMsgId)
	srcFld := mv.ChildText(wbxml.MOSrcFldId)
	dstFld := mv.ChildText(wbxml.MODstFldId)

	response := func(status, dstMsg string) *wbxml.Node {
		return wbxml.Elem(wbxml.MOResponse,
			wbxml.Str(wbxml.MOSrcMsgId, srcMsg),
			wbxml.Str(wbxml.MOStatus, status),
			wbxml.Str(wbxml.MODstMsgId, dstMsg))
	}

	srcFolderID, srcFldErr := strconv.ParseInt(srcFld, 10, 64)
	dstFolderID, dstFldErr := strconv.ParseInt(dstFld, 10, 64)
	uid64, uidErr := strconv.ParseUint(srcMsg, 10, 32)
	if srcFldErr != nil || uidErr != nil {
		return response(moveStatusInvalidSource, srcMsg)
	}
	if dstFldErr != nil {
		return response(moveStatusInvalidDest, srcMsg)
	}
	if srcFolderID == dstFolderID {
		return response(moveStatusSameSrcDest, srcMsg)
	}
	exists, err := st.FolderExists(dstFolderID)
	if err != nil {
		st.LogSwallowedError("activesync.move-destination", err)
		return response(moveStatusFailed, srcMsg)
	}
	if !exists {
		return response(moveStatusInvalidDest, srcMsg)
	}
	info, err := st.MoveMessage(srcFolderID, uint32(uid64), dstFolderID)
	if err != nil {
		// The source no longer resolves (a stale client id) or the move failed.
		return response(moveStatusInvalidSource, srcMsg)
	}
	return response(moveStatusSuccess, strconv.FormatUint(uint64(info.UID), 10))
}

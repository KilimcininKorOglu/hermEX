package activesync

import (
	"net/http"
	"strconv"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// MoveItems status codes (MS-ASCMD). Note success is 3, not 1.
const (
	moveStatusInvalidSource = "1"
	moveStatusInvalidDest   = "2"
	moveStatusSuccess       = "3"
	moveStatusSameSrcDest   = "4"
)

// handleMoveItems answers the MS-ASCMD MoveItems command, moving each message from
// its source folder to a destination folder. The destination server id (the
// message's new per-folder UID) is returned so the client can re-key the item.
func (s *Server) handleMoveItems(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	info, err := st.MoveMessage(srcFolderID, uint32(uid64), dstFolderID)
	if err != nil {
		// The source no longer resolves (a stale client id) or the move failed.
		return response(moveStatusInvalidSource, srcMsg)
	}
	return response(moveStatusSuccess, strconv.FormatUint(uint64(info.UID), 10))
}

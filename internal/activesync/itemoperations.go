package activesync

import (
	"net/http"
	"strconv"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// ItemOperations status codes (MS-ASCMD): success, a conversion/retrieval failure,
// and a malformed-request error.
const (
	ioStatusSuccess  = "1"
	ioStatusConvFail = "14"  // CONVERSIONFAILED — the item could not be retrieved
	ioStatusProtocol = "155" // PROTOCOLERROR — the request was malformed
)

// handleItemOperations answers the MS-ASCMD ItemOperations command. v1 serves Fetch
// for a mailbox message: given its folder and server id it returns the full message
// as a MIME body — the retrieval a client makes after Sync delivered only a
// truncated body. (Attachment Fetch by FileReference, EmptyFolderContents, and Move
// are later work.)
func (s *Server) handleItemOperations(w http.ResponseWriter, r *http.Request, sess *session) {
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

	var fetches []*wbxml.Node
	for _, op := range root.Children {
		if op.Tag == wbxml.IOFetch {
			fetches = append(fetches, fetchMessage(st, op))
		}
	}
	writeWBXML(w, wbxml.Elem(wbxml.IOItemOperations,
		wbxml.Str(wbxml.IOStatus, ioStatusSuccess),
		wbxml.Elem(wbxml.IOResponse, fetches...)))
}

// fetchMessage resolves one Fetch to a mailbox message and renders its full MIME
// body. The folder and server id echo the request so the client can match the
// response; a missing or unaddressable message reports a per-Fetch error status
// with no body. The server id is the message UID, matching Sync's id scheme.
func fetchMessage(st *objectstore.Store, fetch *wbxml.Node) *wbxml.Node {
	collID := fetch.ChildText(wbxml.ASCollectionID)
	serverID := fetch.ChildText(wbxml.ASServerID)
	folderID, ferr := strconv.ParseInt(collID, 10, 64)
	uid64, uerr := strconv.ParseUint(serverID, 10, 32)
	if collID == "" || serverID == "" || ferr != nil || uerr != nil {
		return wbxml.Elem(wbxml.IOFetch, wbxml.Str(wbxml.IOStatus, ioStatusProtocol))
	}
	raw, err := st.GetMessageRaw(folderID, uint32(uid64))
	if err != nil {
		return wbxml.Elem(wbxml.IOFetch,
			wbxml.Str(wbxml.IOStatus, ioStatusConvFail),
			wbxml.Str(wbxml.ASCollectionID, collID),
			wbxml.Str(wbxml.ASServerID, serverID))
	}
	return wbxml.Elem(wbxml.IOFetch,
		wbxml.Str(wbxml.IOStatus, ioStatusSuccess),
		wbxml.Str(wbxml.ASCollectionID, collID),
		wbxml.Str(wbxml.ASServerID, serverID),
		wbxml.Str(wbxml.ASClass, "Email"),
		wbxml.Elem(wbxml.IOProperties,
			wbxml.Elem(wbxml.ABBody,
				wbxml.Str(wbxml.ABType, "4"),
				wbxml.Str(wbxml.ABEstimatedDataSize, strconv.Itoa(len(raw))),
				wbxml.Opaque(wbxml.ABData, raw))))
}

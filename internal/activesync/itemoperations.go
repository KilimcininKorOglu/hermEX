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

// handleItemOperations answers the MS-ASCMD ItemOperations command: a batch of
// Fetch (a mailbox message's MIME body, or an attachment by FileReference) and
// EmptyFolderContents operations. Move is not implemented (deferred).
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

	var responses []*wbxml.Node
	for _, op := range root.Children {
		switch op.Tag {
		case wbxml.IOFetch:
			// A Fetch carrying a FileReference retrieves an attachment; one with a
			// collection and server id retrieves the message itself.
			if ref := op.ChildText(wbxml.ABFileReference); ref != "" {
				responses = append(responses, fetchAttachment(st, ref))
			} else {
				responses = append(responses, fetchMessage(st, op))
			}
		case wbxml.IOEmptyFolderContents:
			responses = append(responses, emptyFolderContents(st, op))
		}
	}
	writeWBXML(w, wbxml.Elem(wbxml.IOItemOperations,
		wbxml.Str(wbxml.IOStatus, ioStatusSuccess),
		wbxml.Elem(wbxml.IOResponse, responses...)))
}

// emptyFolderContents empties a folder's messages (MS-ASCMD ItemOperations
// EmptyFolderContents) and, when the request's Options carry DeleteSubFolders, also
// removes its subfolders. The collection id echoes the request so the client can
// match the response.
func emptyFolderContents(st *objectstore.Store, op *wbxml.Node) *wbxml.Node {
	collID := op.ChildText(wbxml.ASCollectionID)
	folderID, err := strconv.ParseInt(collID, 10, 64)
	if collID == "" || err != nil {
		return wbxml.Elem(wbxml.IOEmptyFolderContents, wbxml.Str(wbxml.IOStatus, ioStatusProtocol))
	}
	deleteSubs := false
	if opts := op.Child(wbxml.IOOptions); opts != nil && opts.Child(wbxml.IODeleteSubFolders) != nil {
		deleteSubs = true
	}
	if err := emptyFolder(st, folderID, deleteSubs); err != nil {
		return wbxml.Elem(wbxml.IOEmptyFolderContents,
			wbxml.Str(wbxml.IOStatus, ioStatusConvFail),
			wbxml.Str(wbxml.ASCollectionID, collID))
	}
	return wbxml.Elem(wbxml.IOEmptyFolderContents,
		wbxml.Str(wbxml.IOStatus, ioStatusSuccess),
		wbxml.Str(wbxml.ASCollectionID, collID))
}

// emptyFolder deletes every message in a folder; when deleteSubs is set it also
// deletes each child folder (and its subtree) through the same primitive FolderDelete
// uses, leaving the target folder itself in place but empty.
func emptyFolder(st *objectstore.Store, folderID int64, deleteSubs bool) error {
	msgs, err := st.ListMessages(folderID)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if err := st.DeleteMessage(folderID, m.UID); err != nil {
			return err
		}
	}
	if !deleteSubs {
		return nil
	}
	folders, err := st.ListFolders()
	if err != nil {
		return err
	}
	for _, f := range folders {
		if f.ParentID != nil && *f.ParentID == folderID {
			if err := st.DeleteFolder(f.ID); err != nil {
				return err
			}
		}
	}
	return nil
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

// fetchAttachment resolves one Fetch by FileReference to an attachment's decoded
// bytes, returning its content type and data. The FileReference echoes the
// request so the client can match the response; a reference that does not resolve
// to an existing attachment reports a per-Fetch error status with no data.
func fetchAttachment(st *objectstore.Store, ref string) *wbxml.Node {
	collID, serverID, index, ok := parseFileRef(ref)
	folderID, ferr := strconv.ParseInt(collID, 10, 64)
	uid64, uerr := strconv.ParseUint(serverID, 10, 32)
	if !ok || ferr != nil || uerr != nil {
		return attachmentError(ref, ioStatusProtocol)
	}
	raw, err := st.GetMessageRaw(folderID, uint32(uid64))
	if err != nil {
		return attachmentError(ref, ioStatusConvFail)
	}
	data, contentType, ok := attachmentContent(raw, index)
	if !ok {
		return attachmentError(ref, ioStatusConvFail)
	}
	return wbxml.Elem(wbxml.IOFetch,
		wbxml.Str(wbxml.IOStatus, ioStatusSuccess),
		wbxml.Str(wbxml.ABFileReference, ref),
		wbxml.Elem(wbxml.IOProperties,
			wbxml.Str(wbxml.ABContentType, contentType),
			wbxml.Opaque(wbxml.IOData, data)))
}

// attachmentError builds an error reply for one attachment Fetch, echoing the
// FileReference so the client can match it.
func attachmentError(ref, status string) *wbxml.Node {
	return wbxml.Elem(wbxml.IOFetch,
		wbxml.Str(wbxml.IOStatus, status),
		wbxml.Str(wbxml.ABFileReference, ref))
}

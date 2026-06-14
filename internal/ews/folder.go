package ews

import (
	"encoding/xml"
	"net/http"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// --- request types (namespace-agnostic: tags carry only the local name, so any
// namespace/prefix a client uses matches) ---

type folderRefs struct {
	Distinguished []refID `xml:"DistinguishedFolderId"`
	Folders       []refID `xml:"FolderId"`
}

type refID struct {
	ID string `xml:"Id,attr"`
}

type getFolderRequest struct {
	FolderIDs folderRefs `xml:"FolderIds"`
}

type findFolderRequest struct {
	Traversal       string     `xml:"Traversal,attr"`
	ParentFolderIDs folderRefs `xml:"ParentFolderIds"`
}

type syncFolderHierarchyRequest struct {
	SyncState string `xml:"SyncState"`
}

// --- response types (the top element declares the messages namespace; children
// inherit it as the default; t:Folder/t:FolderId carry the types namespace) ---

type getFolderResponse struct {
	XMLName  xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetFolderResponse"`
	Messages []folderResponseMessage `xml:"ResponseMessages>GetFolderResponseMessage"`
}

type findFolderResponse struct {
	XMLName  xml.Name                    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindFolderResponse"`
	Messages []findFolderResponseMessage `xml:"ResponseMessages>FindFolderResponseMessage"`
}

type syncFolderHierarchyResponse struct {
	XMLName  xml.Name                       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderHierarchyResponse"`
	Messages []syncHierarchyResponseMessage `xml:"ResponseMessages>SyncFolderHierarchyResponseMessage"`
}

type folderResponseMessage struct {
	ResponseClass string       `xml:"ResponseClass,attr"`
	ResponseCode  string       `xml:"ResponseCode"`
	Folders       *foldersWrap `xml:"Folders,omitempty"`
}

type findFolderResponseMessage struct {
	ResponseClass string          `xml:"ResponseClass,attr"`
	ResponseCode  string          `xml:"ResponseCode"`
	RootFolder    *findRootFolder `xml:"RootFolder,omitempty"`
}

type syncHierarchyResponseMessage struct {
	ResponseClass             string           `xml:"ResponseClass,attr"`
	ResponseCode              string           `xml:"ResponseCode"`
	SyncState                 string           `xml:"SyncState"`
	IncludesLastFolderInRange bool             `xml:"IncludesLastFolderInRange"`
	Changes                   hierarchyChanges `xml:"Changes"`
}

// foldersWrap holds a <Folders> list; each child is an oxews.Folder carrying its
// own types-namespace element name.
type foldersWrap struct {
	Folders []oxews.Folder
}

type findRootFolder struct {
	TotalItemsInView        int  `xml:"TotalItemsInView,attr"`
	IncludesLastItemInRange bool `xml:"IncludesLastItemInRange,attr"`
	// In Find* responses the collection under m:RootFolder is in the types
	// namespace (t:Folders), unlike the messages-namespace m:Folders of GetFolder.
	Folders foldersWrap `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folders"`
}

// hierarchyChanges is the m:Changes wrapper (messages namespace, inherited); the
// individual change elements are in the types namespace (t:Create/t:Delete),
// which is how clients key the change type.
type hierarchyChanges struct {
	Create []createFolderChange `xml:"http://schemas.microsoft.com/exchange/services/2006/types Create"`
	Delete []deleteFolderChange `xml:"http://schemas.microsoft.com/exchange/services/2006/types Delete"`
}

type createFolderChange struct {
	Folder oxews.Folder
}

type deleteFolderChange struct {
	FolderID oxews.FolderID `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

// --- handlers ---

// handleGetFolder answers GetFolder: each requested folder (distinguished id or
// opaque id) yields a folder element or a per-folder error response message.
func (s *Server) handleGetFolder(w http.ResponseWriter, inner []byte, sess *session) {
	var req getFolderRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetFolder: "+err.Error())
		return
	}
	st, all, ok := openFolders(w, sess)
	if !ok {
		return
	}
	defer st.Close()
	idx := folderIndex(all)

	var msgs []folderResponseMessage
	for _, tgt := range resolveTargets(req.FolderIDs) {
		if !tgt.ok {
			msgs = append(msgs, folderResponseMessage{ResponseClass: "Error", ResponseCode: tgt.code})
			continue
		}
		f, code, err := folderElement(st, tgt.fid, idx, all)
		switch {
		case err != nil:
			msgs = append(msgs, folderResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"})
		case code != "":
			msgs = append(msgs, folderResponseMessage{ResponseClass: "Error", ResponseCode: code})
		default:
			msgs = append(msgs, folderResponseMessage{
				ResponseClass: "Success", ResponseCode: "NoError",
				Folders: &foldersWrap{Folders: []oxews.Folder{f}},
			})
		}
	}
	writeResponse(w, getFolderResponse{Messages: msgs})
}

// handleFindFolder answers FindFolder: it enumerates the children of each parent
// (Shallow = direct children, Deep = the whole subtree).
func (s *Server) handleFindFolder(w http.ResponseWriter, inner []byte, sess *session) {
	var req findFolderRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "FindFolder: "+err.Error())
		return
	}
	st, all, ok := openFolders(w, sess)
	if !ok {
		return
	}
	defer st.Close()
	deep := strings.EqualFold(req.Traversal, "Deep")

	var msgs []findFolderResponseMessage
	for _, tgt := range resolveTargets(req.ParentFolderIDs) {
		if !tgt.ok {
			msgs = append(msgs, findFolderResponseMessage{ResponseClass: "Error", ResponseCode: tgt.code})
			continue
		}
		elems, err := folderElements(st, collectChildren(all, tgt.fid, deep), all)
		if err != nil {
			msgs = append(msgs, findFolderResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"})
			continue
		}
		msgs = append(msgs, findFolderResponseMessage{
			ResponseClass: "Success", ResponseCode: "NoError",
			RootFolder: &findRootFolder{
				TotalItemsInView:        len(elems),
				IncludesLastItemInRange: true,
				Folders:                 foldersWrap{Folders: elems},
			},
		})
	}
	writeResponse(w, findFolderResponse{Messages: msgs})
}

// handleSyncFolderHierarchy answers SyncFolderHierarchy: an empty or stale
// SyncState reports every folder as a Create (a fresh prime); a matching
// SyncState reports the folders added/removed since, by diffing the live folder
// set against the snapshot persisted under the previous SyncState.
func (s *Server) handleSyncFolderHierarchy(w http.ResponseWriter, inner []byte, sess *session) {
	var req syncFolderHierarchyRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "SyncFolderHierarchy: "+err.Error())
		return
	}
	st, all, ok := openFolders(w, sess)
	if !ok {
		return
	}
	defer st.Close()
	state, err := loadState(st)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}

	cur := make([]int64, len(all))
	curSet := make(map[int64]bool, len(all))
	for i, f := range all {
		cur[i] = f.ID
		curSet[f.ID] = true
	}

	var changes hierarchyChanges
	primed := req.SyncState != "" && req.SyncState == state.HierarchyState
	if !primed {
		elems, err := folderElements(st, all, all)
		if err != nil {
			writeSOAPFault(w, "ErrorInternalServerError", err.Error())
			return
		}
		for _, e := range elems {
			changes.Create = append(changes.Create, createFolderChange{Folder: e})
		}
	} else {
		prevSet := make(map[int64]bool, len(state.HierarchyFolders))
		for _, id := range state.HierarchyFolders {
			prevSet[id] = true
		}
		for _, f := range all {
			if !prevSet[f.ID] {
				e, err := buildFolderElem(st, f, all)
				if err != nil {
					writeSOAPFault(w, "ErrorInternalServerError", err.Error())
					return
				}
				changes.Create = append(changes.Create, createFolderChange{Folder: e})
			}
		}
		for _, id := range state.HierarchyFolders {
			if !curSet[id] {
				changes.Delete = append(changes.Delete, deleteFolderChange{
					FolderID: oxews.FolderID{ID: oxews.EncodeFolderID(id)},
				})
			}
		}
	}

	newToken := nextSyncState(state.HierarchyState)
	state.HierarchyState = newToken
	state.HierarchyFolders = cur
	if err := saveState(st, state); err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}

	writeResponse(w, syncFolderHierarchyResponse{
		Messages: []syncHierarchyResponseMessage{{
			ResponseClass:             "Success",
			ResponseCode:              "NoError",
			SyncState:                 newToken,
			IncludesLastFolderInRange: true,
			Changes:                   changes,
		}},
	})
}

// --- helpers ---

// openFolders opens the session's store and lists its folders, writing a Fault
// and returning ok=false on error.
func openFolders(w http.ResponseWriter, sess *session) (*objectstore.Store, []objectstore.FolderInfo, bool) {
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return nil, nil, false
	}
	all, err := st.ListFolders()
	if err != nil {
		st.Close()
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return nil, nil, false
	}
	return st, all, true
}

type folderTarget struct {
	fid  int64
	ok   bool
	code string // when ok is false, the per-folder error code to report
}

// resolveTargets resolves the requested folder ids — distinguished names via the
// built-in map, opaque ids via the id codec. An unresolved id carries the right
// error code: a distinguished name we do not map is a folder this mailbox lacks
// (ErrorFolderNotFound), while an undecodable opaque id is malformed
// (ErrorInvalidRequest).
func resolveTargets(refs folderRefs) []folderTarget {
	var out []folderTarget
	for _, d := range refs.Distinguished {
		if fid, ok := distinguishedFolders[strings.ToLower(d.ID)]; ok {
			out = append(out, folderTarget{fid: fid, ok: true})
		} else {
			// A distinguished id is a fixed EWS enum; one we do not map is a
			// folder this mailbox does not have (recoverable-items, search
			// folders, voicemail), not a bad request. ErrorFolderNotFound lets a
			// client that probes the whole distinguished set (enumerating the
			// mailbox root) skip the absent folders instead of aborting the
			// entire batch.
			out = append(out, folderTarget{code: "ErrorFolderNotFound"})
		}
	}
	for _, f := range refs.Folders {
		if fid, err := oxews.DecodeFolderID(f.ID); err == nil {
			out = append(out, folderTarget{fid: fid, ok: true})
		} else {
			out = append(out, folderTarget{code: "ErrorInvalidRequest"})
		}
	}
	return out
}

// folderElement resolves one folder id to its element, returning a non-empty
// error code when the folder is absent. The IPM subtree root is synthesized
// (it is not itself in the listing).
func folderElement(st *objectstore.Store, fid int64, idx map[int64]objectstore.FolderInfo, all []objectstore.FolderInfo) (oxews.Folder, string, error) {
	if fid == mapi.PrivateFIDIPMSubtree || fid == mapi.PrivateFIDRoot {
		return syntheticRoot(all), "", nil
	}
	info, ok := idx[fid]
	if !ok {
		return oxews.Folder{}, "ErrorItemNotFound", nil
	}
	f, err := buildFolderElem(st, info, all)
	return f, "", err
}

// folderElements builds folder elements for a slice of folder infos.
func folderElements(st *objectstore.Store, infos, all []objectstore.FolderInfo) ([]oxews.Folder, error) {
	out := make([]oxews.Folder, 0, len(infos))
	for _, info := range infos {
		f, err := buildFolderElem(st, info, all)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// buildFolderElem renders a folder element with its live item counts and child
// count.
func buildFolderElem(st *objectstore.Store, info objectstore.FolderInfo, all []objectstore.FolderInfo) (oxews.Folder, error) {
	total, unread, err := st.CountMessages(info.ID)
	if err != nil {
		return oxews.Folder{}, err
	}
	cn, err := st.FolderMaxChangeNumber(info.ID)
	if err != nil {
		return oxews.Folder{}, err
	}
	// A top-level folder (nil store parent) hangs off the IPM subtree root, so
	// clients can place it under the mailbox root when building the tree.
	parent := info.ParentID
	if parent == nil {
		root := int64(mapi.PrivateFIDIPMSubtree)
		parent = &root
	}
	return oxews.BuildFolder(oxews.FolderInput{
		FolderID:     info.ID,
		ParentID:     parent,
		ChangeNumber: cn,
		DisplayName:  info.DisplayName,
		Total:        total,
		Unread:       unread,
		Children:     childCount(all, info.ID),
	}), nil
}

// syntheticRoot renders the IPM subtree root, which is not itself enumerated by
// ListFolders; its children are the top-level folders.
func syntheticRoot(all []objectstore.FolderInfo) oxews.Folder {
	return oxews.BuildFolder(oxews.FolderInput{
		FolderID:    mapi.PrivateFIDIPMSubtree,
		DisplayName: "Top of Information Store",
		Children:    childCount(all, mapi.PrivateFIDIPMSubtree),
	})
}

// folderIndex maps folder id to its info.
func folderIndex(all []objectstore.FolderInfo) map[int64]objectstore.FolderInfo {
	m := make(map[int64]objectstore.FolderInfo, len(all))
	for _, f := range all {
		m[f.ID] = f
	}
	return m
}

// childCount counts the direct children of a folder. The IPM subtree root's
// children are the top-level folders (reported with a nil ParentID).
func childCount(all []objectstore.FolderInfo, fid int64) int {
	root := fid == mapi.PrivateFIDIPMSubtree || fid == mapi.PrivateFIDRoot
	n := 0
	for _, f := range all {
		if root {
			if f.ParentID == nil {
				n++
			}
		} else if f.ParentID != nil && *f.ParentID == fid {
			n++
		}
	}
	return n
}

// collectChildren returns a parent's children (Shallow) or its whole subtree
// (Deep). The IPM subtree root's direct children are the top-level folders.
func collectChildren(all []objectstore.FolderInfo, parentFid int64, deep bool) []objectstore.FolderInfo {
	root := parentFid == mapi.PrivateFIDIPMSubtree || parentFid == mapi.PrivateFIDRoot
	var out []objectstore.FolderInfo
	for _, f := range all {
		isChild := f.ParentID != nil && *f.ParentID == parentFid
		if root {
			isChild = f.ParentID == nil
		}
		if isChild {
			out = append(out, f)
			if deep {
				out = append(out, collectChildren(all, f.ID, true)...)
			}
		}
	}
	return out
}

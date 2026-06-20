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
	// Mailbox names another mailbox to target the folder in (MS-OXWSCDATA
	// DistinguishedFolderId/Mailbox). Absent for the caller's own mailbox.
	Mailbox *delegateMailbox `xml:"Mailbox"`
}

type getFolderRequest struct {
	FolderShape folderShape `xml:"FolderShape"`
	FolderIDs   folderRefs  `xml:"FolderIds"`
}

// folderShape carries the requested BaseShape and the AdditionalProperties FieldURI
// list. v1 reads it only to detect a folder:PermissionSet request — the base shape
// is otherwise always served as the same folder subset.
type folderShape struct {
	BaseShape            string `xml:"BaseShape"`
	AdditionalProperties struct {
		FieldURIs []struct {
			URI string `xml:"FieldURI,attr"`
		} `xml:"FieldURI"`
	} `xml:"AdditionalProperties"`
}

// wantsPermissionSet reports whether the shape's AdditionalProperties requested
// folder:PermissionSet. Permissions are returned only when explicitly asked for,
// never in the Default or AllProperties base shape.
func (sh folderShape) wantsPermissionSet() bool {
	for _, fu := range sh.AdditionalProperties.FieldURIs {
		if fu.URI == "folder:PermissionSet" {
			return true
		}
	}
	return false
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
	cache := s.newStoreCache()
	defer cache.closeAll()
	wantPerms := req.FolderShape.wantsPermissionSet()

	var msgs []folderResponseMessage
	for _, tgt := range resolveTargets(req.FolderIDs) {
		if !tgt.ok {
			msgs = append(msgs, folderErr(tgt.code))
			continue
		}
		st, all, isOwn, code := cache.open(sess, tgt.mailbox)
		if code != "" {
			msgs = append(msgs, folderErr(code))
			continue
		}
		// A delegated (non-own) mailbox is gated per folder: the caller must hold at
		// least visibility on the folder, the same right the ROP enforcement path uses.
		if !isOwn {
			rights, err := st.ResolvePermission(tgt.fid, sess.user)
			if err != nil {
				msgs = append(msgs, folderErr("ErrorInternalServerError"))
				continue
			}
			if rights&mapi.FrightsVisible == 0 {
				msgs = append(msgs, folderErr("ErrorAccessDenied"))
				continue
			}
		}
		f, code, err := folderElement(st, tgt.fid, folderIndex(all), all)
		switch {
		case err != nil:
			msgs = append(msgs, folderErr("ErrorInternalServerError"))
			continue
		case code != "":
			msgs = append(msgs, folderErr(code))
			continue
		}
		if wantPerms {
			ps, err := folderPermissionSet(st, tgt.fid)
			if err != nil {
				msgs = append(msgs, folderErr("ErrorInternalServerError"))
				continue
			}
			f.PermissionSet = ps
		}
		msgs = append(msgs, folderResponseMessage{
			ResponseClass: "Success", ResponseCode: "NoError",
			Folders: &foldersWrap{Folders: []oxews.Folder{f}},
		})
	}
	writeResponse(w, getFolderResponse{Messages: msgs})
}

// folderPermissionSet reads a folder's access-control list as a wire PermissionSet.
// The always-present Default and Anonymous members are synthesized at no rights when
// the folder stores no row for them, so a reader always sees them (mirroring the
// permission table the store presents elsewhere).
func folderPermissionSet(st *objectstore.Store, fid int64) (*oxews.PermissionSet, error) {
	entries, err := st.ListPermissions(fid)
	if err != nil {
		return nil, err
	}
	haveDefault, haveAnon := false, false
	for _, e := range entries {
		switch e.MemberID {
		case mapi.MemberIDDefault:
			haveDefault = true
		case mapi.MemberIDAnonymous:
			haveAnon = true
		}
	}
	if !haveDefault {
		entries = append(entries, objectstore.PermissionEntry{MemberID: mapi.MemberIDDefault, Name: "default", Rights: mapi.RightsNone})
	}
	if !haveAnon {
		entries = append(entries, objectstore.PermissionEntry{MemberID: mapi.MemberIDAnonymous, Name: "anonymous", Rights: mapi.RightsNone})
	}
	perms := make([]oxews.Permission, 0, len(entries))
	for _, e := range entries {
		perms = append(perms, oxews.PermissionFromRights(e.MemberID, e.Name, e.Rights))
	}
	return &oxews.PermissionSet{Permissions: perms}, nil
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
	s.icsSync(sess.user, "folder-hierarchy")
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
	fid     int64
	ok      bool
	code    string // when ok is false, the per-folder error code to report
	mailbox string // target mailbox SMTP from the ref's Mailbox child; "" = the caller's own
}

// refMailbox returns the target SMTP a folder reference names via its Mailbox child,
// or "" when it targets the caller's own mailbox.
func refMailbox(m *delegateMailbox) string {
	if m != nil {
		return m.EmailAddress
	}
	return ""
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
			out = append(out, folderTarget{fid: fid, ok: true, mailbox: refMailbox(d.Mailbox)})
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
		if fid, mb, err := oxews.DecodeFolderID(f.ID); err == nil {
			// The mailbox embedded in the opaque id is authoritative (the id is
			// self-contained); fall back to the Mailbox element when the id carries none.
			if mb == "" {
				mb = refMailbox(f.Mailbox)
			}
			out = append(out, folderTarget{fid: fid, ok: true, mailbox: mb})
		} else {
			out = append(out, folderTarget{code: "ErrorInvalidRequest"})
		}
	}
	return out
}

// storeCache opens mailbox stores on demand within one request and closes them all at
// the end, so a multi-folder request that crosses mailboxes opens each distinct store
// only once.
type storeCache struct {
	s       *Server
	stores  map[string]*objectstore.Store
	folders map[string][]objectstore.FolderInfo
}

func (s *Server) newStoreCache() *storeCache {
	return &storeCache{
		s:       s,
		stores:  map[string]*objectstore.Store{},
		folders: map[string][]objectstore.FolderInfo{},
	}
}

// open resolves a target mailbox (the caller's own when mailbox is empty or names the
// caller), opens it once, and returns its folder list and whether it is the caller's
// own mailbox. A non-empty code names the per-folder error to report instead.
func (c *storeCache) open(sess *session, mailbox string) (*objectstore.Store, []objectstore.FolderInfo, bool, string) {
	dir := sess.mailbox
	isOwn := true
	if mailbox != "" && !strings.EqualFold(mailbox, sess.user) {
		path, ok := c.s.accounts.Resolve(mailbox)
		if !ok {
			return nil, nil, false, "ErrorNonExistentMailbox"
		}
		if path != sess.mailbox {
			dir, isOwn = path, false
		}
	}
	if st, ok := c.stores[dir]; ok {
		return st, c.folders[dir], isOwn, ""
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		return nil, nil, false, "ErrorInternalServerError"
	}
	all, err := st.ListFolders()
	if err != nil {
		st.Close()
		return nil, nil, false, "ErrorInternalServerError"
	}
	c.stores[dir] = st
	c.folders[dir] = all
	return st, all, isOwn, ""
}

func (c *storeCache) closeAll() {
	for _, st := range c.stores {
		st.Close()
	}
}

// openForItem opens the mailbox an item id targets — the caller's own when the id
// carries no mailbox — and enforces the caller's access to the item's folder. need is
// the frights bit the operation requires (read, edit, delete). A non-empty code names
// the per-item error to report; own-mailbox access is never gated. This is the single
// chokepoint every item-id-driven handler routes through, so a delegated id can never
// be misapplied to the caller's own store.
func (c *storeCache) openForItem(sess *session, id oxews.ItemID, need uint32) (*objectstore.Store, string) {
	st, _, isOwn, code := c.open(sess, id.Mailbox)
	if code != "" {
		return nil, code
	}
	if !isOwn {
		rights, err := st.ResolvePermission(id.FolderID, sess.user)
		if err != nil {
			return nil, "ErrorInternalServerError"
		}
		if rights&need == 0 {
			return nil, "ErrorAccessDenied"
		}
	}
	return st, ""
}

// folderErr builds a per-folder error response message.
func folderErr(code string) folderResponseMessage {
	return folderResponseMessage{ResponseClass: "Error", ResponseCode: code}
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

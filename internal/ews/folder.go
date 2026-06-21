package ews

import (
	"encoding/xml"
	"errors"
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
		if tgt.public {
			// The public folders root carries no grant of its own (its children do),
			// so it cannot go through the per-folder visibility gate below — it is a
			// distinguished, always-present container rendered synthetically.
			msgs = append(msgs, s.getPublicRoot(cache, sess))
			continue
		}
		st, all, isOwn, code := cache.open(sess, tgt.mailbox)
		if code == codePublicAbsent {
			code = "ErrorFolderNotFound" // a public folder whose domain store is gone
		}
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
		idMailbox := ""
		if !isOwn {
			idMailbox = tgt.mailbox
		}
		f, code, err := folderElement(st, tgt.fid, folderIndex(all), all, idMailbox)
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
// getPublicRoot renders the public folders root as a synthetic, always-present
// container whose child count is the folders the caller may see. It never denies:
// an un-provisioned domain simply has zero visible children, the same uniform
// answer FindFolder gives. The root carries no grant of its own, so it is never
// run through the per-folder visibility gate.
func (s *Server) getPublicRoot(cache *storeCache, sess *session) folderResponseMessage {
	st, all, _, code := cache.open(sess, publicMailboxToken)
	if code != "" && code != codePublicAbsent {
		return folderErr(code)
	}
	count := 0
	if code == "" {
		for _, f := range all {
			if f.ParentID != nil {
				continue
			}
			rights, err := st.ResolvePermission(f.ID, sess.user)
			if err == nil && rights&mapi.FrightsVisible != 0 {
				count++
			}
		}
	}
	f := oxews.BuildFolder(oxews.FolderInput{
		FolderID:    int64(mapi.PublicFIDIPMSubtree),
		DisplayName: "Public Folders",
		Children:    count,
		Mailbox:     publicMailboxToken,
	})
	return folderResponseMessage{
		ResponseClass: "Success", ResponseCode: "NoError",
		Folders: &foldersWrap{Folders: []oxews.Folder{f}},
	}
}

// emptyFindFolder is a successful FindFolder result with no folders — the answer
// for a public folders root the caller can see nothing in (or whose domain has no
// public store): publicfoldersroot is a distinguished folder the protocol treats as
// always present, so "empty for you" is the honest, uniform response.
func emptyFindFolder() findFolderResponseMessage {
	return findFolderResponseMessage{
		ResponseClass: "Success", ResponseCode: "NoError",
		RootFolder: &findRootFolder{IncludesLastItemInRange: true},
	}
}

func (s *Server) handleFindFolder(w http.ResponseWriter, inner []byte, sess *session) {
	var req findFolderRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "FindFolder: "+err.Error())
		return
	}
	cache := s.newStoreCache()
	defer cache.closeAll()
	deep := strings.EqualFold(req.Traversal, "Deep")

	var msgs []findFolderResponseMessage
	for _, tgt := range resolveTargets(req.ParentFolderIDs) {
		if !tgt.ok {
			msgs = append(msgs, findFolderResponseMessage{ResponseClass: "Error", ResponseCode: tgt.code})
			continue
		}
		st, all, isOwn, code := cache.open(sess, tgt.mailbox)
		if tgt.public && code == codePublicAbsent {
			// The caller's domain has no public store: the public folders root is
			// simply empty for them, not an error (same response as a provisioned
			// store with no folders the caller may see).
			msgs = append(msgs, emptyFindFolder())
			continue
		}
		if code == codePublicAbsent {
			code = "ErrorFolderNotFound" // a specific public folder in an un-provisioned domain
		}
		if code != "" {
			msgs = append(msgs, findFolderResponseMessage{ResponseClass: "Error", ResponseCode: code})
			continue
		}
		var children []objectstore.FolderInfo
		if tgt.public {
			// all is already the public store's IPM-subtree folders (ListFolders roots
			// there). Shallow takes the top-level ones (nil parent under the subtree),
			// Deep takes the whole set. The IPM subtree itself carries no grant, so
			// filter its children per folder.
			var under []objectstore.FolderInfo
			for _, f := range all {
				if deep || f.ParentID == nil {
					under = append(under, f)
				}
			}
			vis, err := filterVisible(st, under, sess.user)
			if err != nil {
				msgs = append(msgs, findFolderResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"})
				continue
			}
			children = vis
		} else {
			// Enumerating another mailbox's subfolders requires visibility on the parent.
			if !isOwn {
				rights, err := st.ResolvePermission(tgt.fid, sess.user)
				if err != nil {
					msgs = append(msgs, findFolderResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"})
					continue
				}
				if rights&mapi.FrightsVisible == 0 {
					msgs = append(msgs, findFolderResponseMessage{ResponseClass: "Error", ResponseCode: "ErrorAccessDenied"})
					continue
				}
			}
			children = collectChildren(all, tgt.fid, deep)
		}
		idMailbox := ""
		if !isOwn {
			idMailbox = tgt.mailbox
		}
		elems, err := folderElements(st, children, all, idMailbox)
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
		elems, err := folderElements(st, all, all, "")
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
				e, err := buildFolderElem(st, f, all, "")
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

// publicMailboxToken is the reserved "mailbox" that routes a folder id to the
// caller's own domain public store instead of a user mailbox. It contains
// characters no SMTP address can, so it never collides with a real mailbox; it is
// only ever encoded inside opaque folder ids, never rendered to the client. The
// store cache resolves it through the publicfolder service, which derives the
// domain from the authenticated caller — so a crafted id can never reach another
// tenant's public store.
const publicMailboxToken = "\x00public-folders"

// codePublicAbsent is an internal store-cache result meaning the caller's domain
// has no public store. It is never written to the wire: the publicfoldersroot
// enumeration maps it to an empty success, and a specific public-folder id maps it
// to ErrorFolderNotFound.
const codePublicAbsent = "\x00public-absent"

type folderTarget struct {
	fid     int64
	ok      bool
	code    string // when ok is false, the per-folder error code to report
	mailbox string // target mailbox SMTP from the ref's Mailbox child; "" = the caller's own
	public  bool   // the publicfoldersroot distinguished id: enumerate the domain public store
}

// filterVisible keeps only the folders the caller may see — those on which their
// effective rights include FrightsVisible — the per-folder ACL gate public-folder
// enumeration needs (grants live on each folder, not on the IPM subtree parent).
func filterVisible(st *objectstore.Store, infos []objectstore.FolderInfo, user string) ([]objectstore.FolderInfo, error) {
	out := make([]objectstore.FolderInfo, 0, len(infos))
	for _, f := range infos {
		rights, err := st.ResolvePermission(f.ID, user)
		if err != nil {
			return nil, err
		}
		if rights&mapi.FrightsVisible != 0 {
			out = append(out, f)
		}
	}
	return out, nil
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
		if strings.EqualFold(d.ID, "publicfoldersroot") {
			// The public folders root is not a folder in the caller's mailbox: it is
			// the caller's domain public store IPM subtree, enumerated per-child by ACL.
			out = append(out, folderTarget{fid: int64(mapi.PublicFIDIPMSubtree), ok: true, mailbox: publicMailboxToken, public: true})
		} else if fid, ok := distinguishedFolders[strings.ToLower(d.ID)]; ok {
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
			// A public-store IPM subtree id (the public root, e.g. minted as a public
			// child's ParentFolderId) is the public folders root, enumerated per-child
			// by ACL — the same as the publicfoldersroot distinguished name.
			public := mb == publicMailboxToken && fid == int64(mapi.PublicFIDIPMSubtree)
			out = append(out, folderTarget{fid: fid, ok: true, mailbox: mb, public: public})
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
	if mailbox == publicMailboxToken {
		return c.openPublic(sess)
	}
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

// openPublic resolves the caller's own domain public store (never another tenant's:
// the domain comes from the authenticated caller, not from any id). It returns
// codePublicAbsent when the domain has no public store, isOwn=false so the per-folder
// ACL gate applies, and caches the handle by directory like any other store.
func (c *storeCache) openPublic(sess *session) (*objectstore.Store, []objectstore.FolderInfo, bool, string) {
	if c.s.Pub == nil {
		return nil, nil, false, codePublicAbsent
	}
	dir := c.s.Pub.DirForCaller(sess.user)
	if dir == "" {
		return nil, nil, false, codePublicAbsent
	}
	if st, ok := c.stores[dir]; ok {
		return st, c.folders[dir], false, ""
	}
	st, err := objectstore.OpenPublicExisting(dir)
	if errors.Is(err, objectstore.ErrNotProvisioned) {
		return nil, nil, false, codePublicAbsent
	}
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
	return st, all, false, ""
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
func folderElement(st *objectstore.Store, fid int64, idx map[int64]objectstore.FolderInfo, all []objectstore.FolderInfo, mailbox string) (oxews.Folder, string, error) {
	if fid == mapi.PrivateFIDIPMSubtree || fid == mapi.PrivateFIDRoot {
		return syntheticRoot(all, mailbox), "", nil
	}
	info, ok := idx[fid]
	if !ok {
		return oxews.Folder{}, "ErrorItemNotFound", nil
	}
	f, err := buildFolderElem(st, info, all, mailbox)
	return f, "", err
}

// folderElements builds folder elements for a slice of folder infos. mailbox is the
// target mailbox SMTP when the folders live in another mailbox (so the minted folder
// ids reopen it); empty for the caller's own.
func folderElements(st *objectstore.Store, infos, all []objectstore.FolderInfo, mailbox string) ([]oxews.Folder, error) {
	out := make([]oxews.Folder, 0, len(infos))
	for _, info := range infos {
		f, err := buildFolderElem(st, info, all, mailbox)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// buildFolderElem renders a folder element with its live item counts and child
// count. mailbox tags the minted folder ids with the target mailbox (empty for own).
func buildFolderElem(st *objectstore.Store, info objectstore.FolderInfo, all []objectstore.FolderInfo, mailbox string) (oxews.Folder, error) {
	total, unread, err := st.CountMessages(info.ID)
	if err != nil {
		return oxews.Folder{}, err
	}
	cn, err := st.FolderMaxChangeNumber(info.ID)
	if err != nil {
		return oxews.Folder{}, err
	}
	// A top-level folder (nil store parent) hangs off the IPM subtree root, so
	// clients can place it under the mailbox root when building the tree. A public
	// store's top-level folders hang off the public IPM subtree (publicfoldersroot),
	// not the private one, so the parent id round-trips to the right root.
	parent := info.ParentID
	if parent == nil {
		root := int64(mapi.PrivateFIDIPMSubtree)
		if mailbox == publicMailboxToken {
			root = int64(mapi.PublicFIDIPMSubtree)
		}
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
		Mailbox:      mailbox,
	}), nil
}

// syntheticRoot renders the IPM subtree root, which is not itself enumerated by
// ListFolders; its children are the top-level folders.
func syntheticRoot(all []objectstore.FolderInfo, mailbox string) oxews.Folder {
	return oxews.BuildFolder(oxews.FolderInput{
		FolderID:    mapi.PrivateFIDIPMSubtree,
		DisplayName: "Top of Information Store",
		Children:    childCount(all, mapi.PrivateFIDIPMSubtree),
		Mailbox:     mailbox,
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

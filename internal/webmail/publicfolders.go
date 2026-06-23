package webmail

import (
	"fmt"
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// publicFolderLink is one public folder in the discovery sidebar, with its live
// message counts and whether the caller may post to it.
type publicFolderLink struct {
	ID      int64
	Name    string
	Total   int
	Unread  int
	CanPost bool
}

// publicFoldersPage backs the read-only public folders browser: the visible
// folders, and (when one is opened) its message list.
type publicFoldersPage struct {
	User       string
	Folders    []publicFolderLink
	Current    string // opened folder's name; "" on the discovery landing
	CurrentFID int64
	Messages   []messageView
	Page       int
	MaxPage    int
	PrevPage   int
	NextPage   int
	Total      int
	Unread     int
}

// publicMessagePage backs the read-only public-folder message reader.
type publicMessagePage struct {
	User   string
	FID    int64
	Detail messageDetail
}

// publicTarget opens the caller's own-domain public store and verifies fid is a
// real public folder the caller may read (FrightsReadAny). The domain is derived
// from the authenticated caller by the publicfolder service, never from the
// request, so a forged fid can never reach another tenant. The caller owns the
// returned store and must Close it when ok is true.
func (s *Server) publicTarget(sess *session, fid int64) (st *objectstore.Store, name string, ok bool) {
	if s.Pub == nil {
		return nil, "", false
	}
	st, ok, err := s.Pub.OpenForCaller(sess.user)
	if err != nil || !ok {
		return nil, "", false
	}
	all, err := st.ListFolders()
	if err != nil {
		st.Close()
		return nil, "", false
	}
	for _, f := range all {
		if f.ID == fid {
			name = f.DisplayName
			break
		}
	}
	if name == "" {
		st.Close()
		return nil, "", false
	}
	rights, err := st.ResolvePermission(fid, sess.user)
	if err != nil || rights&mapi.FrightsReadAny == 0 {
		st.Close()
		return nil, "", false
	}
	return st, name, true
}

// listVisiblePublicFolders returns the public folders the caller may see (those
// granting FrightsVisible) with live message counts — the data behind both the
// mailbox sidebar's public-folders section and the public-folders browser.
// Returns nil when public folders are unconfigured or the caller's domain has no
// public store. The caller's domain is derived by the publicfolder service from
// the authenticated user, never from the request.
func (s *Server) listVisiblePublicFolders(sess *session) []publicFolderLink {
	if s.Pub == nil {
		return nil
	}
	st, ok, err := s.Pub.OpenForCaller(sess.user)
	if err != nil || !ok {
		return nil
	}
	defer st.Close()
	all, err := st.ListFolders()
	if err != nil {
		return nil
	}
	var out []publicFolderLink
	for _, f := range all {
		rights, err := st.ResolvePermission(f.ID, sess.user)
		if err != nil || rights&mapi.FrightsVisible == 0 {
			continue
		}
		total, unread, _ := st.CountMessages(f.ID)
		out = append(out, publicFolderLink{
			ID: f.ID, Name: f.DisplayName, Total: total, Unread: unread,
			CanPost: rights&mapi.FrightsCreate != 0,
		})
	}
	return out
}

// handlePublicFolders renders the public folders browser: the folders the caller
// may see, plus the message list of the one addressed by ?fid. The per-fid open
// re-checks read access (via publicTarget) server-side, so a forged fid cannot
// read a folder the caller may not.
func (s *Server) handlePublicFolders(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	page := publicFoldersPage{User: sess.user, Folders: s.listVisiblePublicFolders(sess)}

	// An opened folder (?fid) re-checks read access server-side; a bad or
	// unreadable fid simply shows the discovery list without an opened folder.
	if fid, err := strconv.ParseInt(r.URL.Query().Get("fid"), 10, 64); err == nil {
		if st, name, ok := s.publicTarget(sess, fid); ok {
			defer st.Close()
			params := listParams{Sort: "date", Dir: "desc", Filter: "all", Page: atoiDefault(r.URL.Query().Get("page"), 1)}
			if res, err := listFolderPage(st, fid, name, params, nil); err == nil {
				page.Current = name
				page.CurrentFID = fid
				page.Messages = res.Messages
				page.Page = res.Page
				page.MaxPage = res.MaxPage
				page.PrevPage = res.PrevPage
				page.NextPage = res.NextPage
				page.Total = res.Total
				page.Unread = res.Unread
			}
		}
	}
	s.render(w, "public_folders", page)
}

// handlePublicMessage renders a public-folder message read-only. It does not mark
// the message \Seen: the folder's flags are shared organization-wide, and this is
// a read-only browse.
func (s *Server) handlePublicMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	fid, err := strconv.ParseInt(r.URL.Query().Get("fid"), 10, 64)
	if err != nil {
		http.Error(w, "bad fid", http.StatusBadRequest)
		return
	}
	uid, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	if err != nil {
		http.Error(w, "bad uid", http.StatusBadRequest)
		return
	}
	st, name, ok := s.publicTarget(sess, fid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	defer st.Close()

	raw, err := st.GetMessageRaw(fid, uint32(uid))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	detail := buildMessageDetail(raw, name, uint32(uid), false, nil, false)
	s.render(w, "public_message", publicMessagePage{User: sess.user, FID: fid, Detail: detail})
}

// handlePublicAttachment streams an attachment part from a public-folder message,
// gated by the same read ACL as the reader.
func (s *Server) handlePublicAttachment(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	fid, err := strconv.ParseInt(r.URL.Query().Get("fid"), 10, 64)
	if err != nil {
		http.Error(w, "bad fid", http.StatusBadRequest)
		return
	}
	uid, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	if err != nil {
		http.Error(w, "bad uid", http.StatusBadRequest)
		return
	}
	path, ok := parsePartPath(r.URL.Query().Get("part"))
	if !ok {
		http.Error(w, "bad part", http.StatusBadRequest)
		return
	}
	st, _, ok := s.publicTarget(sess, fid)
	if !ok {
		http.NotFound(w, r)
		return
	}
	defer st.Close()

	raw, err := st.GetMessageRaw(fid, uint32(uid))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	part, ok := mime.ParseStructure(raw).PartAt(path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	content, err := part.DecodedContent()
	if err != nil {
		http.Error(w, "cannot decode attachment", http.StatusInternalServerError)
		return
	}
	filename := part.Filename()
	if filename == "" {
		filename = "attachment"
	}
	w.Header().Set("Content-Type", part.Type+"/"+part.Subtype)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.Write(content)
}

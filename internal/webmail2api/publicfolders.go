package webmail2api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// publicFolderJSON is one public folder the caller may see, with live message
// counts and whether the caller may post to it.
type publicFolderJSON struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Total   int    `json:"total"`
	Unread  int    `json:"unread"`
	CanPost bool   `json:"can_post"`
}

// handleGetPublicFolders lists the public folders in the caller's OWN domain that
// the caller may see (FrightsVisible), each with live counts. The domain is
// derived server-side by the publicfolder service from the caller's address,
// never from the request, so no request can reach another tenant's tree.
func (s *Server) handleGetPublicFolders(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	// Per-user read state lives in the caller's OWN store; the shared public flag
	// is org-wide and intentionally never set on read, so the unread badge is
	// computed against this user's read set, not the shared flag.
	readSet := s.publicReadSet(c.Mailbox, publicOwner(c.Email))
	folders := []publicFolderJSON{}
	if s.Pub != nil {
		if st, ok, err := s.Pub.OpenForCaller(c.Email); err == nil && ok {
			defer st.Close()
			user := strings.ToLower(c.Email)
			if all, err := st.ListFolders(); err == nil {
				for _, f := range all {
					rights, err := st.ResolvePermission(f.ID, user)
					if err != nil || rights&mapi.FrightsVisible == 0 {
						continue
					}
					total, unread := 0, 0
					if msgs, err := st.ListMessages(f.ID); err == nil {
						total = len(msgs)
						for _, m := range msgs {
							if !readSet[m.ID] {
								unread++
							}
						}
					}
					folders = append(folders, publicFolderJSON{
						ID: f.ID, Name: f.DisplayName, Total: total, Unread: unread,
						CanPost: rights&mapi.FrightsCreate != 0,
					})
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"owner": publicOwner(c.Email), "folders": folders})
}

// handlePublicFolderMessages lists one public folder's messages, re-checking read
// access (FrightsReadAny) server-side so a forged fid cannot read a folder the
// caller may not.
func (s *Server) handlePublicFolderMessages(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	fid, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad fid"})
		return
	}
	st, _, ok := s.publicTarget(c.Email, fid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	defer st.Close()
	// Read state is per-user (from the caller's own store). It replaces the shared
	// public flag rather than OR-ing with it; that flag is never set on read.
	readSet := s.publicReadSet(c.Mailbox, publicOwner(c.Email))
	out := []mailJSON{}
	if msgs, err := st.ListMessages(fid); err == nil {
		for _, m := range msgs {
			out = append(out, mailJSON{
				ID: strconv.FormatUint(uint64(m.UID), 10), From: m.Sender, FromName: m.Sender,
				Subject: m.Subject, Date: m.InternalDate.Format(time.RFC3339),
				Read: readSet[m.ID], Starred: m.Flags&objectstore.FlagFlagged != 0,
				Folder: "public", Size: int(m.Size),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"emails": out, "total": len(out)})
}

// handlePublicMessage returns one public-folder message's full detail, gated by
// the same read ACL. It is read-only: the folder's flags are shared org-wide, so
// reading does NOT mark \Seen.
func (s *Server) handlePublicMessage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	fid, err := strconv.ParseInt(r.URL.Query().Get("fid"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad fid"})
		return
	}
	uid64, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad uid"})
		return
	}
	st, _, ok := s.publicTarget(c.Email, fid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uint32(uid64))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	// Reading marks the message read for THIS user only, recorded in the caller's
	// own store and never written back to the shared public store (its flags are
	// org-wide). Best-effort: a tracking failure must not fail the read itself.
	if info, err := st.MessageByUID(fid, uint32(uid64)); err == nil {
		s.markPublicRead(c.Mailbox, publicOwner(c.Email), info.ID)
	}
	writeJSON(w, http.StatusOK, buildMailDetail(raw, "public", uint32(uid64)))
}

// publicTarget opens the caller's own-domain public store and verifies fid is a
// folder the caller may read (FrightsReadAny). The domain is derived server-side
// from the caller, so a forged fid can never reach another tenant. The caller
// owns the returned store and must Close it when ok is true.
func (s *Server) publicTarget(email string, fid int64) (*objectstore.Store, string, bool) {
	if s.Pub == nil {
		return nil, "", false
	}
	st, ok, err := s.Pub.OpenForCaller(email)
	if err != nil || !ok {
		return nil, "", false
	}
	all, err := st.ListFolders()
	if err != nil {
		st.Close()
		return nil, "", false
	}
	name := ""
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
	rights, err := st.ResolvePermission(fid, strings.ToLower(email))
	if err != nil || rights&mapi.FrightsReadAny == 0 {
		st.Close()
		return nil, "", false
	}
	return st, name, true
}

// publicReadSet returns the caller's per-user read set for the given public
// owner, read from the caller's OWN store (mailbox). It is best-effort: any
// failure to open the store or read the table yields an empty set (everything
// unread), never an error to the client, so public-folder browsing degrades
// gracefully when per-user read state is unavailable.
func (s *Server) publicReadSet(mailbox, owner string) map[int64]bool {
	own, err := objectstore.Open(mailbox)
	if err != nil {
		return map[int64]bool{}
	}
	defer own.Close()
	set, err := own.PublicReadSet(owner)
	if err != nil {
		return map[int64]bool{}
	}
	return set
}

// markPublicRead records, in the caller's OWN store (mailbox), that the user has
// read a public message. Best-effort by design: public-folder reading must
// succeed even when the per-user read state cannot be written.
func (s *Server) markPublicRead(mailbox, owner string, messageID int64) {
	own, err := objectstore.Open(mailbox)
	if err != nil {
		return
	}
	defer own.Close()
	_ = own.MarkPublicMessageRead(owner, messageID)
}

// publicOwner returns the caller's domain — the key the SPA shows beside the
// public-folder section.
func publicOwner(email string) string {
	if _, domain, ok := strings.Cut(strings.ToLower(email), "@"); ok {
		return domain
	}
	return ""
}

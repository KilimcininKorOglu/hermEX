package webmail

import (
	"archive/zip"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

func (s *Server) handleAttachment(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	folder := r.URL.Query().Get("folder")
	uid64, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	if err != nil {
		http.Error(w, "bad uid", http.StatusBadRequest)
		return
	}
	path, ok := parsePartPath(r.URL.Query().Get("part"))
	if !ok {
		http.Error(w, "bad part", http.StatusBadRequest)
		return
	}

	// Open the own mailbox, or a shared mailbox the caller selected (?mbox),
	// validated and access-checked server-side.
	mbox := mboxParam(r)
	var st *objectstore.Store
	if mbox == "" {
		if st, err = objectstore.Open(sess.mailboxPath); err != nil {
			http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
			return
		}
	} else {
		var sok bool
		if st, _, sok = s.openSharedFor(sess, mbox); !sok {
			http.NotFound(w, r)
			return
		}
	}
	defer st.Close()

	folders, err := st.ListFolders()
	if err != nil {
		http.Error(w, "cannot read folders", http.StatusInternalServerError)
		return
	}
	folderID, found := resolveFolder(folders, folder)
	if !found {
		http.NotFound(w, r)
		return
	}
	// A shared folder's attachment is gated by the same read ACL as its reader.
	if mbox != "" {
		if rights, err := st.ResolvePermission(folderID, sess.user); err != nil || rights&mapi.FrightsReadAny == 0 {
			http.NotFound(w, r)
			return
		}
	}
	raw, err := st.GetMessageRaw(folderID, uint32(uid64))
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

// displayedAttachments returns the attachment list a reader sees for a message:
// every non-body part minus the inline images the HTML body folds in via cid:
// (the same filtering messageDetail applies), so a download-all matches the UI
// rather than dumping a newsletter's inline logos into the archive.
func displayedAttachments(root *mime.Part) []attachmentView {
	bodyPart, isHTML, atts := selectParts(root, false)
	if isHTML && bodyPart != nil {
		if content, err := bodyPart.DecodedContent(); err == nil {
			if _, inlined := inlineCIDImages(toUTF8(content, bodyPart.Params["charset"]), root); len(inlined) > 0 {
				atts = dropSections(atts, inlined)
			}
		}
	}
	return atts
}

// handleAttachmentsZip streams every displayed attachment of one message as a zip
// (the reference's "download all as zip"). It mirrors handleAttachment's mailbox
// resolution and read ACL, then zips the same filtered attachment list the reader
// sees, de-duplicating colliding filenames.
func (s *Server) handleAttachmentsZip(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	folder := r.URL.Query().Get("folder")
	uid64, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	if err != nil {
		http.Error(w, "bad uid", http.StatusBadRequest)
		return
	}
	mbox := mboxParam(r)
	var st *objectstore.Store
	if mbox == "" {
		if st, err = objectstore.Open(sess.mailboxPath); err != nil {
			http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
			return
		}
	} else {
		var sok bool
		if st, _, sok = s.openSharedFor(sess, mbox); !sok {
			http.NotFound(w, r)
			return
		}
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		http.Error(w, "cannot read folders", http.StatusInternalServerError)
		return
	}
	folderID, found := resolveFolder(folders, folder)
	if !found {
		http.NotFound(w, r)
		return
	}
	if mbox != "" {
		if rights, err := st.ResolvePermission(folderID, sess.user); err != nil || rights&mapi.FrightsReadAny == 0 {
			http.NotFound(w, r)
			return
		}
	}
	raw, err := st.GetMessageRaw(folderID, uint32(uid64))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	root := mime.ParseStructure(raw)
	atts := displayedAttachments(root)
	if len(atts) == 0 {
		http.Error(w, "no attachments", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="attachments.zip"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	used := map[string]int{}
	for _, a := range atts {
		path, ok := parsePartPath(a.Section)
		if !ok {
			continue
		}
		part, ok := root.PartAt(path)
		if !ok {
			continue
		}
		content, err := part.DecodedContent()
		if err != nil {
			continue
		}
		f, err := zw.Create(dedupZipName(used, a.Filename))
		if err != nil {
			return
		}
		f.Write(content)
	}
}

// dedupZipName returns a zip entry name unique within used, appending " (n)"
// before the extension when a filename repeats (two parts can share a name).
func dedupZipName(used map[string]int, name string) string {
	if name == "" {
		name = "attachment"
	}
	n := used[name]
	used[name] = n + 1
	if n == 0 {
		return name
	}
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		return fmt.Sprintf("%s (%d)%s", name[:dot], n, name[dot:])
	}
	return fmt.Sprintf("%s (%d)", name, n)
}

// parsePartPath parses a "1.2" section path into a numeric slice.
func parsePartPath(s string) ([]int, bool) {
	if s == "" {
		return nil, false
	}
	var path []int
	for p := range strings.SplitSeq(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 {
			return nil, false
		}
		path = append(path, n)
	}
	return path, true
}

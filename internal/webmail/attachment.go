package webmail

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mime"
	"hermex/internal/store"
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

	st, err := store.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
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

package webmail2api

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// handleAttachment streams the Nth attachment of a message (the same walk order
// collectAttachments assigns).
func (s *Server) handleAttachment(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	index, _ := strconv.Atoi(r.URL.Query().Get("index"))
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	root := mime.ParseStructure(raw)
	var found *mime.Part
	idx := 0
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil || found != nil {
			return
		}
		name := p.DispParams["filename"]
		if name == "" {
			name = p.Params["name"]
		}
		if p.Type != "multipart" && (p.Disposition == "attachment" || name != "") {
			if idx == index {
				found = p
				return
			}
			idx++
		}
		for _, ch := range p.Children {
			walk(ch)
		}
	}
	walk(root)
	if found == nil {
		http.Error(w, "attachment not found", http.StatusNotFound)
		return
	}
	body, err := found.DecodedContent()
	if err != nil {
		http.Error(w, "cannot decode", http.StatusInternalServerError)
		return
	}
	filename := found.Filename()
	if filename == "" {
		filename = "attachment"
	}
	w.Header().Set("Content-Type", found.Type+"/"+found.Subtype)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	_, _ = w.Write(body)
}

// handleExport serves a message as a downloadable .eml file.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", "attachment; filename=\"message.eml\"")
	_, _ = w.Write(raw)
}

// handleRecover restores a message from Deleted Items back to the Inbox.
func (s *Server) handleRecover(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	if _, err := st.MoveMessage(fid, uid, mapi.PrivateFIDInbox); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "recover failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"folder": "inbox"})
}

// handleLabels sets a message's labels (stored as its categories).
func (s *Server) handleLabels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string   `json:"id"`
		Labels []string `json:"labels"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, fid, uid, ok := s.locate(w, r, req.ID)
	if !ok {
		return
	}
	defer st.Close()
	info, err := st.MessageByUID(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err := st.SetCategories(info.ID, req.Labels); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not set labels"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSearch scans the mail folders for messages matching the query, on
// subject/sender (and body when present).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	results := []mailJSON{}
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"emails": results, "total": 0, "query": q})
		return
	}
	for slug, fid := range searchFolders() {
		msgs, err := st.ListMessages(fid)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			hay := strings.ToLower(m.Subject + " " + m.Sender)
			if !strings.Contains(hay, q) {
				if raw, err := st.GetMessageRaw(fid, m.UID); err == nil {
					if root := mime.ParseStructure(raw); !strings.Contains(strings.ToLower(bestBody(root)), q) {
						continue
					}
				} else {
					continue
				}
			}
			results = append(results, mailJSON{
				ID: messageID(slug, m.UID), From: m.Sender, FromName: m.Sender,
				Subject: m.Subject, Date: m.InternalDate.Format("2006-01-02T15:04:05Z07:00"),
				Read: m.Flags&objectstore.FlagSeen != 0, Starred: m.Flags&objectstore.FlagFlagged != 0,
				Folder: slug, Size: int(m.Size),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"emails": results, "total": len(results), "query": q})
}

// searchFolders is the set of mail folders the search scans.
func searchFolders() map[string]int64 {
	return map[string]int64{
		"inbox":  mapi.PrivateFIDInbox,
		"sent":   mapi.PrivateFIDSentItems,
		"drafts": mapi.PrivateFIDDraft,
		"trash":  mapi.PrivateFIDDeletedItems,
		"spam":   mapi.PrivateFIDJunk,
	}
}

// handleThreads returns the inbox grouped into simple subject threads.
func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	// Threading is not yet implemented server-side; the SPA falls back to the flat
	// list. Return an empty thread set so the page renders.
	writeJSON(w, http.StatusOK, map[string]any{"threads": []any{}})
}

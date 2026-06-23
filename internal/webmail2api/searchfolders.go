package webmail2api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// searchFolderJSON is the SPA's SearchFolder: a named, saved search over the mail
// folders. Empty criteria fields are ignored (match anything).
type searchFolderJSON struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	From          string   `json:"from,omitempty"`
	Subject       string   `json:"subject,omitempty"`
	Body          string   `json:"body,omitempty"`
	DateFrom      string   `json:"date_from,omitempty"`
	DateTo        string   `json:"date_to,omitempty"`
	HasAttachment bool     `json:"has_attachment,omitempty"`
	BaseFolders   []string `json:"base_folders,omitempty"`
}

func readSearchFolders(m map[string]json.RawMessage) []searchFolderJSON {
	var sf []searchFolderJSON
	if raw, ok := m["webmail2SearchFolders"]; ok {
		_ = json.Unmarshal(raw, &sf)
	}
	return sf
}

func writeSearchFolders(m map[string]json.RawMessage, sf []searchFolderJSON) {
	raw, _ := json.Marshal(sf)
	m["webmail2SearchFolders"] = raw
}

func (s *Server) handleGetSearchFolders(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		return map[string]any{"search_folders": readSearchFolders(m)}, false
	})
}

func (s *Server) handlePostSearchFolder(w http.ResponseWriter, r *http.Request) {
	var in searchFolderJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		sf := readSearchFolders(m)
		in.ID = randomHex()[:8]
		sf = append(sf, in)
		writeSearchFolders(m, sf)
		return in, true
	})
}

func (s *Server) handlePutSearchFolder(w http.ResponseWriter, r *http.Request) {
	var in searchFolderJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	id := r.PathValue("id")
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		sf := readSearchFolders(m)
		for i := range sf {
			if sf[i].ID == id {
				in.ID = id
				sf[i] = in
				break
			}
		}
		writeSearchFolders(m, sf)
		return in, true
	})
}

func (s *Server) handleDeleteSearchFolder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		sf := readSearchFolders(m)
		kept := sf[:0]
		for _, f := range sf {
			if f.ID != id {
				kept = append(kept, f)
			}
		}
		writeSearchFolders(m, kept)
		return map[string]bool{"ok": true}, true
	})
}

// handleSearchFolderResults runs a saved search and returns the matching mail.
func (s *Server) handleSearchFolderResults(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		var sf *searchFolderJSON
		for _, f := range readSearchFolders(m) {
			if f.ID == id {
				sf = &f
				break
			}
		}
		if sf == nil {
			return map[string]any{"emails": []mailJSON{}, "total": 0}, false
		}
		results := runSearchFolder(st, *sf)
		return map[string]any{"emails": results, "total": len(results)}, false
	})
}

// runSearchFolder scans the saved search's folders and returns the messages
// matching all its set criteria.
func runSearchFolder(st *objectstore.Store, sf searchFolderJSON) []mailJSON {
	folders := searchFolders()
	results := []mailJSON{}
	for slug, fid := range folders {
		if len(sf.BaseFolders) > 0 && !containsFold(sf.BaseFolders, slug) {
			continue
		}
		msgs, err := st.ListMessages(fid)
		if err != nil {
			continue
		}
		for _, msg := range msgs {
			if !matchSearchFolder(st, fid, sf, msg) {
				continue
			}
			results = append(results, mailJSON{
				ID: messageID(slug, msg.UID), From: msg.Sender, FromName: msg.Sender,
				Subject: msg.Subject, Date: msg.InternalDate.Format(time.RFC3339),
				Read: msg.Flags&objectstore.FlagSeen != 0, Starred: msg.Flags&objectstore.FlagFlagged != 0,
				Folder: slug, Size: int(msg.Size),
			})
		}
	}
	return results
}

// matchSearchFolder reports whether a message satisfies every set criterion. The
// raw message is fetched only when a body or attachment criterion needs it.
func matchSearchFolder(st *objectstore.Store, fid int64, sf searchFolderJSON, m objectstore.MessageInfo) bool {
	if sf.From != "" && !strings.Contains(strings.ToLower(m.Sender), strings.ToLower(sf.From)) {
		return false
	}
	if sf.Subject != "" && !strings.Contains(strings.ToLower(m.Subject), strings.ToLower(sf.Subject)) {
		return false
	}
	if d := parseSearchDate(sf.DateFrom); !d.IsZero() && m.InternalDate.Before(d) {
		return false
	}
	if d := parseSearchDate(sf.DateTo); !d.IsZero() && m.InternalDate.After(d.Add(24*time.Hour)) {
		return false
	}
	if sf.Body != "" || sf.HasAttachment {
		raw, err := st.GetMessageRaw(fid, m.UID)
		if err != nil {
			return false
		}
		root := mime.ParseStructure(raw)
		if sf.Body != "" && !strings.Contains(strings.ToLower(bestBody(root)), strings.ToLower(sf.Body)) {
			return false
		}
		if sf.HasAttachment && len(collectAttachments(root, nil)) == 0 {
			return false
		}
	}
	return true
}

// parseSearchDate parses a YYYY-MM-DD or RFC3339 date, returning the zero time
// when empty or unparseable.
func parseSearchDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// containsFold reports case-insensitive membership.
func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

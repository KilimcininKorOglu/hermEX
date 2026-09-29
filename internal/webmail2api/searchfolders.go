package webmail2api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// A saved search is a search folder in the mailbox's Finder folder. The store
// keeps its criteria and works out its contents, so a saved search made here is
// the search folder Outlook and EWS list, and one they made is a saved search here.

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
	// Custom marks a search folder whose criteria another client wrote in a shape
	// the fields above cannot express. An edit renames it and keeps its criteria.
	Custom bool `json:"custom,omitempty"`
}

// legacySearchFoldersKey is the settings key saved searches were kept under
// before they became store search folders.
const legacySearchFoldersKey = "webmail2SearchFolders"

// errBadSearchFolder reports a saved search naming a folder the mailbox lacks.
var errBadSearchFolder = errors.New("webmail2api: saved search names an unknown folder")

// withSearchFolders runs fn against the caller's store once the saved searches
// the settings blob still holds have become search folders.
func (s *Server) withSearchFolders(w http.ResponseWriter, r *http.Request, fn func(st *objectstore.Store, loc *time.Location) (any, error)) {
	loc := s.callerZone(r)
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		moved, err := moveLegacySearches(st, m, loc)
		if err != nil {
			return settingsFailure{status: http.StatusInternalServerError, msg: "saved searches unavailable", event: "webmail.saved_search_move", err: err}, false
		}
		resp, err := fn(st, loc)
		if err != nil {
			return searchFolderFailure(err), false
		}
		return resp, moved
	})
}

// searchFolderFailure maps a store error to the answer a client gets.
func searchFolderFailure(err error) settingsFailure {
	switch {
	case errors.Is(err, objectstore.ErrNotFound), errors.Is(err, objectstore.ErrNotSearchFolder):
		return settingsFailure{status: http.StatusNotFound, msg: "saved search not found"}
	case errors.Is(err, objectstore.ErrFolderExists):
		return settingsFailure{status: http.StatusConflict, msg: "a saved search with that name exists"}
	case errors.Is(err, errBadSearchFolder):
		return settingsFailure{status: http.StatusBadRequest, msg: "unknown folder"}
	}
	return settingsFailure{status: http.StatusInternalServerError, msg: "saved search failed", event: "webmail.saved_search", err: err}
}

// moveLegacySearches turns each saved search the settings blob holds into a
// search folder and drops the blob's copy, reporting whether it changed the blob.
func moveLegacySearches(st *objectstore.Store, m map[string]json.RawMessage, loc *time.Location) (bool, error) {
	raw, ok := m[legacySearchFoldersKey]
	if !ok {
		return false, nil
	}
	var legacy []searchFolderJSON
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return false, err
	}
	for _, sf := range legacy {
		if _, err := createSavedSearch(st, sf, loc); err != nil {
			return false, err
		}
	}
	delete(m, legacySearchFoldersKey)
	return true, nil
}

func (s *Server) handleGetSearchFolders(w http.ResponseWriter, r *http.Request) {
	s.withSearchFolders(w, r, func(st *objectstore.Store, loc *time.Location) (any, error) {
		folders, err := st.SearchFolderChildren(int64(mapi.PrivateFIDFinder))
		if err != nil {
			return nil, err
		}
		out := make([]searchFolderJSON, 0, len(folders))
		for _, f := range folders {
			sf, err := readSearchFolder(st, f.ID, f.DisplayName, loc)
			if err != nil {
				return nil, err
			}
			out = append(out, sf)
		}
		return map[string]any{"search_folders": out}, nil
	})
}

func (s *Server) handlePostSearchFolder(w http.ResponseWriter, r *http.Request) {
	var in searchFolderJSON
	if err := decodeJSON(r, &in); err != nil || in.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSearchFolders(w, r, func(st *objectstore.Store, loc *time.Location) (any, error) {
		return createSavedSearch(st, in, loc)
	})
}

func (s *Server) handlePutSearchFolder(w http.ResponseWriter, r *http.Request) {
	var in searchFolderJSON
	fid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || decodeJSON(r, &in) != nil || in.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSearchFolders(w, r, func(st *objectstore.Store, loc *time.Location) (any, error) {
		return updateSavedSearch(st, fid, in, loc)
	})
}

func (s *Server) handleDeleteSearchFolder(w http.ResponseWriter, r *http.Request) {
	fid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSearchFolders(w, r, func(st *objectstore.Store, _ *time.Location) (any, error) {
		if err := requireSavedSearch(st, fid); err != nil {
			return nil, err
		}
		if err := st.DeleteFolder(fid); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

// handleSearchFolderResults returns the mail a saved search holds.
func (s *Server) handleSearchFolderResults(w http.ResponseWriter, r *http.Request) {
	fid, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "saved search not found"})
		return
	}
	s.withSearchFolders(w, r, func(st *objectstore.Store, _ *time.Location) (any, error) {
		if err := requireSavedSearch(st, fid); err != nil {
			return nil, err
		}
		results, err := savedSearchMail(st, fid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"emails": results, "total": len(results)}, nil
	})
}

// requireSavedSearch refuses a folder that is not a search folder in Finder, so a
// client id can name only a saved search.
func requireSavedSearch(st *objectstore.Store, fid int64) error {
	folders, err := st.SearchFolderChildren(int64(mapi.PrivateFIDFinder))
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(folders, func(f objectstore.FolderInfo) bool { return f.ID == fid }) {
		return objectstore.ErrNotFound
	}
	return nil
}

// createSavedSearch makes a search folder in Finder with a saved search's name
// and criteria.
func createSavedSearch(st *objectstore.Store, in searchFolderJSON, loc *time.Location) (searchFolderJSON, error) {
	criteria, err := savedSearchCriteria(st, in, loc)
	if err != nil {
		return in, err
	}
	fid, err := st.CreateSearchFolder(int64(mapi.PrivateFIDFinder), in.Name)
	if err != nil {
		return in, err
	}
	if err := st.SetSearchCriteria(fid, criteria); err != nil {
		return in, errors.Join(err, st.DeleteFolder(fid))
	}
	in.ID = strconv.FormatInt(fid, 10)
	in.Custom = false
	return in, nil
}

// updateSavedSearch renames a saved search and replaces its criteria. A search
// folder whose criteria the fields cannot express keeps them and is only renamed,
// so an edit here never widens a search another client defined.
func updateSavedSearch(st *objectstore.Store, fid int64, in searchFolderJSON, loc *time.Location) (searchFolderJSON, error) {
	if err := requireSavedSearch(st, fid); err != nil {
		return in, err
	}
	current, err := readSearchFolder(st, fid, in.Name, loc)
	if err != nil {
		return in, err
	}
	if err := st.SetFolderName(fid, in.Name); err != nil {
		return in, err
	}
	if current.Custom {
		return current, nil
	}
	criteria, err := savedSearchCriteria(st, in, loc)
	if err != nil {
		return in, err
	}
	if err := st.SetSearchCriteria(fid, criteria); err != nil {
		return in, err
	}
	in.ID = strconv.FormatInt(fid, 10)
	in.Custom = false
	return in, nil
}

// savedSearchCriteria is the running search a saved search's fields name, over
// the folders it names or, when it names none, the mail folders a search covers.
func savedSearchCriteria(st *objectstore.Store, in searchFolderJSON, loc *time.Location) (objectstore.SearchCriteria, error) {
	var scope []int64
	for _, slug := range in.BaseFolders {
		fid, ok := resolveFolder(st, slug)
		if !ok {
			return objectstore.SearchCriteria{}, errBadSearchFolder
		}
		scope = append(scope, fid)
	}
	if len(scope) == 0 {
		scope = defaultSearchScope()
	}
	r := savedSearchRestriction(in, loc)
	return objectstore.SearchCriteria{Restriction: &r, Scope: scope, Flags: mapi.SearchRestart | mapi.SearchShallow}, nil
}

// defaultSearchScope is the mail folders a saved search naming none looks in.
func defaultSearchScope() []int64 {
	var scope []int64
	for _, f := range searchFolders() {
		scope = append(scope, f.fid)
	}
	return scope
}

// readSearchFolder reads a search folder as a saved search.
func readSearchFolder(st *objectstore.Store, fid int64, name string, loc *time.Location) (searchFolderJSON, error) {
	sf := searchFolderJSON{ID: strconv.FormatInt(fid, 10), Name: name}
	c, _, err := st.GetSearchCriteria(fid)
	if err != nil {
		return sf, err
	}
	if c.Restriction == nil || !readSavedSearch(*c.Restriction, &sf, loc) {
		return searchFolderJSON{ID: sf.ID, Name: name, Custom: true}, nil
	}
	if !slices.Equal(c.Scope, defaultSearchScope()) {
		for _, f := range c.Scope {
			sf.BaseFolders = append(sf.BaseFolders, folderSlug(st, f))
		}
	}
	return sf, nil
}

// folderSlug is the name the SPA addresses a folder by: a well-known slug, or a
// custom folder's display name.
func folderSlug(st *objectstore.Store, fid int64) string {
	for _, f := range searchFolders() {
		if f.fid == fid {
			return f.slug
		}
	}
	props, err := st.GetFolderProperties(fid, mapi.PrDisplayName)
	if err != nil {
		return ""
	}
	name, _ := props.Get(mapi.PrDisplayName)
	s, _ := name.(string)
	return s
}

// savedSearchMail returns the mail a search folder holds, newest first and
// bounded as a search is.
func savedSearchMail(st *objectstore.Store, fid int64) ([]mailJSON, error) {
	ids, err := st.SearchFolderMessageIDs(fid)
	if err != nil {
		return nil, err
	}
	byFolder := map[int64]map[int64]bool{}
	for _, id := range ids {
		folder, _, ok, err := st.MessageIndexLocation(id)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if byFolder[folder] == nil {
			byFolder[folder] = map[int64]bool{}
		}
		byFolder[folder][id] = true
	}
	var rows []foundMail
	for folder, want := range byFolder {
		msgs, err := st.ListMessages(folder)
		if err != nil {
			return nil, err
		}
		slug := folderSlug(st, folder)
		for _, m := range msgs {
			if want[m.ID] {
				rows = append(rows, foundMail{MessageInfo: m, slug: slug})
			}
		}
	}
	return newestMail(rows), nil
}

// foundMail is a message a saved search holds, with the slug of its folder.
type foundMail struct {
	objectstore.MessageInfo
	slug string
}

// newestMail renders the newest maxSearchResults rows.
func newestMail(rows []foundMail) []mailJSON {
	slices.SortFunc(rows, func(a, b foundMail) int { return b.InternalDate.Compare(a.InternalDate) })
	rows = rows[:min(len(rows), maxSearchResults)]
	out := make([]mailJSON, 0, len(rows))
	for _, msg := range rows {
		out = append(out, mailJSON{
			ID: messageID(msg.slug, msg.UID), From: msg.Sender, FromName: msg.Sender,
			Subject: msg.Subject, Date: msg.InternalDate.Format(time.RFC3339),
			Read: msg.Flags&objectstore.FlagSeen != 0, Starred: msg.Flags&objectstore.FlagFlagged != 0,
			Folder: msg.slug, Size: int(msg.Size),
		})
	}
	return out
}

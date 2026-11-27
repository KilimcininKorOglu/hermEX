package webmail2api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"hermex/internal/objectstore"
)

// favoritesKey is the PrWebmailSettings blob key holding the user's pinned
// favourite folders, each by its folder slug (slugOf), so a rename keeps the pin
// and two folders of one name are two different pins.
const favoritesKey = "webmail2FavoriteFolders"

// legacyFavoritesKey held the pins by display name before folders were addressed
// by id. It is read once, converted into favoritesKey and removed.
const legacyFavoritesKey = "webmail2Favorites"

// favoritesLoad is the pinned folder slugs and whether loading them rewrote m.
type favoritesLoad struct {
	slugs    []string
	migrated bool
}

// loadFavorites returns the pinned folder slugs, converting a legacy list of
// display names into slugs (and reporting that m changed) the first time it is
// read. A name converts to the folder it reached before, the folder the user
// pinned; a name that no longer reaches a folder is dropped. A stored list that
// does not decode is an error: converting or toggling it would replace the pins
// it holds with an empty list.
func loadFavorites(st *objectstore.Store, m map[string]json.RawMessage) (favoritesLoad, error) {
	fav := []string{}
	if _, ok := m[favoritesKey]; ok {
		if err := decodeSetting(m, favoritesKey, &fav); err != nil {
			return favoritesLoad{}, err
		}
		if fav == nil {
			fav = []string{}
		}
		return favoritesLoad{slugs: fav}, nil
	}
	var names []string
	if err := decodeSetting(m, legacyFavoritesKey, &names); err != nil {
		return favoritesLoad{}, err
	}
	if names == nil {
		return favoritesLoad{slugs: fav}, nil
	}
	for _, name := range names {
		if fid, found := legacyFolder(st, name); found && !slices.Contains(fav, slugOf(fid)) {
			fav = append(fav, slugOf(fid))
		}
	}
	if err := putSetting(m, favoritesKey, fav); err != nil {
		return favoritesLoad{}, err
	}
	delete(m, legacyFavoritesKey)
	return favoritesLoad{slugs: fav, migrated: true}, nil
}

// legacyFolder resolves a folder reference stored before folders were addressed
// by id: a built-in slug, else the display name as it resolved then.
func legacyFolder(st *objectstore.Store, name string) (int64, bool) {
	if fid, ok := folderFID(strings.ToLower(name)); ok {
		return fid, true
	}
	return folderByName(st, name)
}

// handleGetFavorites returns the user's pinned favourite folders, the slugs of
// the folders the sidebar shows in a "Favorites" section above the folder list.
func (s *Server) handleGetFavorites(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		fl, err := loadFavorites(st, m)
		if err != nil {
			return settingsUnreadable(err), false
		}
		return map[string]any{"favorites": fl.slugs}, fl.migrated
	})
}

// handleToggleFavorite pins or unpins a folder in the user's sidebar favourites,
// persisted in the webmail settings blob. The folder is named by its slug and
// must exist, so a pin always names a real folder.
func (s *Server) handleToggleFavorite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &in); err != nil || in.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		fid, ok := resolveFolder(st, in.ID)
		if !ok {
			return settingsFailure{status: http.StatusNotFound, msg: "folder not found"}, false
		}
		fl, err := loadFavorites(st, m)
		if err != nil {
			return settingsUnreadable(err), false
		}
		fav := fl.slugs
		slug := slugOf(fid)
		if i := slices.Index(fav, slug); i >= 0 {
			fav = slices.Delete(fav, i, i+1)
		} else {
			fav = append(fav, slug)
		}
		if err := putSetting(m, favoritesKey, fav); err != nil {
			return settingsUnsaved(err), false
		}
		return map[string]any{"favorites": fav}, true
	})
}

// unpinFolder removes a deleted folder's pin, so the sidebar does not point at a
// folder that is no longer there. A rename needs no upkeep: the pin is the id.
//
// It reports its own failure rather than the caller's: the folder operation has
// already succeeded by the time this runs, and a pin left behind must not be
// answered as a failed delete.
func (s *Server) unpinFolder(st *objectstore.Store, mailbox string, fid int64) {
	unlock := s.lockSettings(mailbox)
	defer unlock()
	m, loaded := settingsForWrite(st)
	if !loaded {
		// The pin is upkeep on a folder operation that already succeeded; writing
		// a blob assembled from an unreadable one would cost far more than a stale
		// pin.
		return
	}
	fl, err := loadFavorites(st, m)
	if err != nil {
		st.LogSwallowedError("favorites.unpin", err)
		return
	}
	fav := fl.slugs
	i := slices.Index(fav, slugOf(fid))
	if i < 0 && !fl.migrated {
		return
	}
	if i >= 0 {
		fav = slices.Delete(fav, i, i+1)
	}
	if err := putSetting(m, favoritesKey, fav); err != nil {
		st.LogSwallowedError("favorites.unpin", err)
		return
	}
	if err := saveSharedSettings(st, m); err != nil {
		st.LogSwallowedError("favorites.unpin", err)
	}
}

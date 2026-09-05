package webmail2api

import (
	"encoding/json"
	"net/http"

	"hermex/internal/objectstore"
)

// favoritesKey is the PrWebmailSettings blob key holding the user's pinned
// favourite folders (a list of custom-folder display names).
const favoritesKey = "webmail2Favorites"

func readFavorites(m map[string]json.RawMessage) []string {
	fav := []string{}
	if raw, ok := m[favoritesKey]; ok {
		_ = json.Unmarshal(raw, &fav)
	}
	if fav == nil {
		fav = []string{}
	}
	return fav
}

func writeFavorites(m map[string]json.RawMessage, fav []string) {
	raw, _ := json.Marshal(fav)
	m[favoritesKey] = raw
}

// handleGetFavorites returns the user's pinned favourite folders, the names the
// sidebar shows in a "Favorites" section above the regular folder list.
func (s *Server) handleGetFavorites(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		return map[string]any{"favorites": readFavorites(m)}, false
	})
}

// handleToggleFavorite pins or unpins a folder in the user's sidebar favourites,
// the old webmail's favorite toggle, persisted in the webmail settings blob.
func (s *Server) handleToggleFavorite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		fav := readFavorites(m)
		idx := -1
		for i, f := range fav {
			if f == in.Name {
				idx = i
				break
			}
		}
		if idx >= 0 {
			fav = append(fav[:idx], fav[idx+1:]...)
		} else {
			fav = append(fav, in.Name)
		}
		writeFavorites(m, fav)
		return map[string]any{"favorites": fav}, true
	})
}

// retitleFavorite keeps the sidebar pins in step with a folder operation. A
// favourite is stored by DISPLAY NAME, so a rename that does not carry the pin
// drops it, and a delete that does not clear it leaves the sidebar pointing at a
// folder that is no longer there. An empty newName removes the pin.
//
// It reports its own failure rather than the caller's: the folder operation has
// already succeeded by the time this runs, and a pin left behind must not be
// answered as a failed rename.
func (s *Server) retitleFavorite(st *objectstore.Store, mailbox, oldName, newName string) {
	unlock := s.lockSettings(mailbox)
	defer unlock()
	m := sharedSettings(st)
	fav := readFavorites(m)
	out := make([]string, 0, len(fav))
	changed := false
	for _, f := range fav {
		switch {
		case f != oldName:
			out = append(out, f)
		case newName != "":
			out = append(out, newName)
			changed = true
		default:
			changed = true
		}
	}
	if !changed {
		return
	}
	writeFavorites(m, out)
	if err := saveSharedSettings(st, m); err != nil {
		st.LogSwallowedError("favorites.retitle", err)
	}
}

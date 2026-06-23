package webmail

import (
	"net/http"
	"strings"
)

// addressBookLimit caps how many directory entries the address-book browser lists
// for one search (higher than the compose autocomplete cap, which is per-keystroke).
const addressBookLimit = 100

// addressBookView is the address-book page model: the search term and its matches.
type addressBookView struct {
	Query   string
	Entries []galSuggestion
}

// handleAddressBook renders the Global Address List browser: a search box and the
// matching directory entries (name + address), each a one-click compose target.
// It reuses the same GAL search the compose autocomplete uses, so what a user can
// browse here matches what "check names" resolves. A directory with no GAL simply
// shows no results.
func (s *Server) handleAddressBook(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessionFrom(r); !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	view := addressBookView{Query: q}
	if q != "" {
		view.Entries = s.suggest(q, addressBookLimit)
	}
	s.render(w, "addressbook", view)
}

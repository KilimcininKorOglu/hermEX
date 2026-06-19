package admin

import "net/http"

// handleListUsers lists every user. This first increment is system-admin only;
// org- and domain-scoped listing is a later refinement.
func (s *Server) handleListUsers(w http.ResponseWriter, _ *http.Request) {
	users, err := s.dir.ListUsers()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, users)
}

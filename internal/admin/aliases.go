package admin

import (
	"encoding/json"
	"net/http"
)

// handleListAliases lists every alias (system administrators only for now).
func (s *Server) handleListAliases(w http.ResponseWriter, _ *http.Request) {
	aliases, err := s.dir.ListAliases()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, aliases)
}

// handleCreateAlias creates an alias that forwards to a primary address (system
// administrators only).
func (s *Server) handleCreateAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Alias string `json:"alias"`
		Main  string `json:"main"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Alias == "" || req.Main == "" {
		http.Error(w, "an alias and a target address are required", http.StatusBadRequest)
		return
	}
	if err := s.dir.CreateAlias(req.Alias, req.Main); err != nil {
		http.Error(w, "could not create alias: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"alias": req.Alias, "main": req.Main})
}

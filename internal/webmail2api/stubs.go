package webmail2api

import "net/http"

// This file holds endpoints with no server-side backing yet. They return a
// well-typed empty/default response (not the generic {} stub) so the SPA renders
// the page cleanly while the feature is built out.

// handleBranding serves the login-page branding. Unauthenticated.
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"app_name":      "hermEX",
		"primary_color": "#4f46e5",
		"tagline":       "Secure self-hosted email",
		"footer_text":   "hermEX",
	})
}

// handleAvatar has no stored avatars yet; 404 lets the SPA show initials.
func (s *Server) handleAvatar(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "no avatar", http.StatusNotFound)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	s.emptyAuthed(w, r, "sessions")
}

func (s *Server) handleSearchFolders(w http.ResponseWriter, r *http.Request) {
	s.emptyAuthed(w, r, "search_folders")
}

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	s.emptyAuthed(w, r, "rooms")
}

func (s *Server) handleFreeBusy(w http.ResponseWriter, r *http.Request) {
	s.emptyAuthed(w, r, "freeBusy")
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	s.emptyAuthed(w, r, "errors")
}

func (s *Server) handleSmimeCert(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"isInvite": false})
}

// emptyAuthed writes {key: []} for an authenticated caller (401 otherwise).
func (s *Server) emptyAuthed(w http.ResponseWriter, r *http.Request, key string) {
	if _, ok := s.session(r); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{key: []any{}})
}

package webmail2api

import (
	"encoding/base64"
	"net/http"
	"strings"

	"hermex/internal/objectstore"
)

// handleGetAvatar serves the caller's own portrait (others get 404, so the SPA
// falls back to initials). The bytes are the cross-protocol photo every protocol
// reads.
func (s *Server) handleGetAvatar(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if email := strings.TrimSpace(r.URL.Query().Get("email")); email != "" && !strings.EqualFold(email, c.Email) {
		http.Error(w, "no avatar", http.StatusNotFound)
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		http.Error(w, "no avatar", http.StatusNotFound)
		return
	}
	defer st.Close()
	photo, _ := st.UserPhoto()
	if photo == nil {
		http.Error(w, "no avatar", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(photo))
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(photo)
}

// handlePutAvatar stores the caller's portrait (an image data URL) as the
// cross-protocol photo property, so it shows in webmail, the GAL, and Outlook.
func (s *Server) handlePutAvatar(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		Avatar string `json:"avatar"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	_, data, ok := decodeImageDataURL(body.Avatar)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected an image data URL"})
		return
	}
	if len(data) > 1<<20 {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "image too large (max 1 MB)"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	if err := st.SetUserPhoto(data); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the avatar"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteAvatar clears the caller's portrait.
func (s *Server) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	_ = st.SetUserPhoto(nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// decodeImageDataURL parses "data:image/<t>;base64,<data>" into its content type
// and bytes. ok is false for a non-image or malformed value.
func decodeImageDataURL(s string) (contentType string, data []byte, ok bool) {
	if !strings.HasPrefix(s, "data:") {
		return "", nil, false
	}
	meta, b64, found := strings.Cut(s[len("data:"):], ",")
	if !found || !strings.Contains(meta, "base64") {
		return "", nil, false
	}
	ct, _, _ := strings.Cut(meta, ";")
	if !strings.HasPrefix(ct, "image/") {
		return "", nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) == 0 {
		return "", nil, false
	}
	return ct, raw, true
}

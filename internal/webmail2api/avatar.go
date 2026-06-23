package webmail2api

import (
	"encoding/base64"
	"net/http"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// photoTag resolves the cross-protocol user-photo named property (the provider
// "photo" property) to its store property tag, allocating an id when create is set.
func photoTag(st *objectstore.Store, create bool) (mapi.PropTag, bool) {
	ids, err := st.GetNamedPropIDs(create, []mapi.PropertyName{mapi.NameUserPhoto})
	if err != nil || len(ids) == 0 || ids[0] == 0 {
		return 0, false
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtBinary)), true
}

// readPhoto returns the mailbox's stored portrait bytes, or nil when none. These
// are the same bytes the GAL serves to Outlook as PR_EMS_AB_THUMBNAIL_PHOTO.
func readPhoto(st *objectstore.Store) []byte {
	tag, ok := photoTag(st, false)
	if !ok {
		return nil
	}
	props, err := st.GetStoreProperties(tag)
	if err != nil {
		return nil
	}
	if v, ok := props.Get(tag); ok {
		if b, ok := v.([]byte); ok && len(b) > 0 {
			return b
		}
	}
	return nil
}

// handleGetAvatar serves the caller's own portrait (others get 404, so the SPA
// falls back to initials).
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
	photo := readPhoto(st)
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
	tag, ok := photoTag(st, true)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not allocate the photo property"})
		return
	}
	if err := st.SetStoreProperties(mapi.PropertyValues{{Tag: tag, Value: data}}); err != nil {
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
	if tag, ok := photoTag(st, false); ok {
		_ = st.SetStoreProperties(mapi.PropertyValues{{Tag: tag, Value: []byte(nil)}})
	}
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

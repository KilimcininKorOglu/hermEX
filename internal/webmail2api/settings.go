package webmail2api

import (
	"encoding/json"
	"net/http"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// sharedSettings reads the shared webmail settings blob (PrWebmailSettings) as a
// generic object so webmail2 can update only the keys it owns without dropping
// fields the server-rendered webmail manages (composeFormat, density, ...). Both
// clients share categories and signatures this way — the user's real settings,
// not a per-client fork.
func sharedSettings(st *objectstore.Store) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	if props, err := st.GetStoreProperties(mapi.PrWebmailSettings); err == nil {
		if v, ok := props.Get(mapi.PrWebmailSettings); ok {
			if str, ok := v.(string); ok && str != "" {
				_ = json.Unmarshal([]byte(str), &m)
			}
		}
	}
	return m
}

func saveSharedSettings(st *objectstore.Store, m map[string]json.RawMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var props mapi.PropertyValues
	props.Set(mapi.PrWebmailSettings, string(b))
	return st.SetStoreProperties(props)
}

// withSettings opens the caller's store and runs fn against its shared settings
// map, persisting it when fn returns true.
func (s *Server) withSettings(w http.ResponseWriter, r *http.Request, fn func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool)) {
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
	m := sharedSettings(st)
	resp, save := fn(st, m)
	if save {
		if err := saveSharedSettings(st, m); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save settings"})
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- Preferences (a UI-toggle map kept under webmail2's own key) ----

func (s *Server) handleGetPreferences(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		prefs := map[string]bool{}
		if raw, ok := m["webmail2Preferences"]; ok {
			_ = json.Unmarshal(raw, &prefs)
		}
		return map[string]any{"preferences": prefs}, false
	})
}

func (s *Server) handlePutPreferences(w http.ResponseWriter, r *http.Request) {
	var prefs map[string]bool
	if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		raw, _ := json.Marshal(prefs)
		m["webmail2Preferences"] = raw
		return map[string]any{"preferences": prefs}, true
	})
}

// ---- Categories (shared shape {name,color}) ----

type categoryJSON struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (s *Server) handleGetCategories(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		cats := []categoryJSON{}
		if raw, ok := m["categories"]; ok {
			_ = json.Unmarshal(raw, &cats)
		}
		return map[string]any{"categories": cats}, false
	})
}

func (s *Server) handlePutCategories(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Categories []categoryJSON `json:"categories"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		raw, _ := json.Marshal(body.Categories)
		m["categories"] = raw
		return map[string]any{"categories": body.Categories}, true
	})
}

// ---- Signatures (webmail stores {id,name,html,isHTML}; the SPA wants
// {name,body,is_html,ord}) ----

type webmailSig struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	HTML   string `json:"html"`
	IsHTML bool   `json:"isHTML"`
}

type signatureJSON struct {
	Name   string `json:"name"`
	Body   string `json:"body"`
	IsHTML bool   `json:"is_html"`
	Ord    int    `json:"ord"`
}

func readSigs(m map[string]json.RawMessage) []webmailSig {
	var sigs []webmailSig
	if raw, ok := m["signatures"]; ok {
		_ = json.Unmarshal(raw, &sigs)
	}
	return sigs
}

func (s *Server) handleGetSignatures(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		sigs := readSigs(m)
		out := make([]signatureJSON, 0, len(sigs))
		for i, sig := range sigs {
			out = append(out, signatureJSON{Name: sig.Name, Body: sig.HTML, IsHTML: sig.IsHTML, Ord: i})
		}
		return map[string]any{"signatures": out}, false
	})
}

func (s *Server) handleGetSignature(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		sigs := readSigs(m)
		body := ""
		if len(sigs) > 0 {
			body = sigs[0].HTML
		}
		return map[string]any{"signature": body}, false
	})
}

func (s *Server) handlePostSignature(w http.ResponseWriter, r *http.Request) {
	var in signatureJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		sigs := readSigs(m)
		updated := false
		for i := range sigs {
			if sigs[i].Name == in.Name {
				sigs[i].HTML, sigs[i].IsHTML = in.Body, in.IsHTML
				updated = true
				break
			}
		}
		if !updated {
			sigs = append(sigs, webmailSig{ID: randomHex()[:8], Name: in.Name, HTML: in.Body, IsHTML: in.IsHTML})
		}
		raw, _ := json.Marshal(sigs)
		m["signatures"] = raw
		return map[string]any{"signature": in}, true
	})
}

func (s *Server) handleDeleteSignature(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		sigs := readSigs(m)
		kept := sigs[:0]
		for _, sig := range sigs {
			if sig.Name != name {
				kept = append(kept, sig)
			}
		}
		raw, _ := json.Marshal(kept)
		m["signatures"] = raw
		return map[string]bool{"ok": true}, true
	})
}

// ---- Templates (webmail2's own key) ----

type templateJSON struct {
	Name    string `json:"name"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	IsHTML  bool   `json:"is_html"`
}

func readTemplates(m map[string]json.RawMessage) []templateJSON {
	var t []templateJSON
	if raw, ok := m["webmail2Templates"]; ok {
		_ = json.Unmarshal(raw, &t)
	}
	return t
}

func (s *Server) handleGetTemplates(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		return map[string]any{"templates": readTemplates(m)}, false
	})
}

func (s *Server) handlePostTemplate(w http.ResponseWriter, r *http.Request) {
	var in templateJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		ts := readTemplates(m)
		updated := false
		for i := range ts {
			if ts[i].Name == in.Name {
				ts[i] = in
				updated = true
				break
			}
		}
		if !updated {
			ts = append(ts, in)
		}
		raw, _ := json.Marshal(ts)
		m["webmail2Templates"] = raw
		return map[string]any{"template": in}, true
	})
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		ts := readTemplates(m)
		kept := ts[:0]
		for _, t := range ts {
			if t.Name != name {
				kept = append(kept, t)
			}
		}
		raw, _ := json.Marshal(kept)
		m["webmail2Templates"] = raw
		return map[string]bool{"ok": true}, true
	})
}

// ---- Profile ----

func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	prof := map[string]any{"email": c.Email, "onboarded": true}
	// The display name, title, department, and phone live in the directory's user
	// properties (keyed by full proptag) — the same fields the GAL and Outlook show.
	if dir, ok := s.auth.(interface {
		GetUserProperties(string) (map[uint32]string, error)
	}); ok {
		if props, err := dir.GetUserProperties(c.Email); err == nil {
			for key, tag := range profileProps() {
				if v := props[uint32(tag)]; v != "" {
					prof[key] = v
				}
			}
		}
	}
	// Storage usage from the store; quota limits are best-effort (0 = unlimited).
	if st, err := objectstore.Open(c.Mailbox); err == nil {
		if used, err := st.MailboxSize(); err == nil {
			prof["quota_used"] = used
		}
		st.Close()
	}
	writeJSON(w, http.StatusOK, prof)
}

// profileProps maps the SPA's editable profile fields to their directory MAPI
// proptags — the cross-protocol properties the GAL and Outlook also read.
func profileProps() map[string]mapi.PropTag {
	return map[string]mapi.PropTag{
		"display_name": mapi.PrDisplayName,
		"title":        mapi.PrTitle,
		"department":   mapi.PrDepartmentName,
		"phone":        mapi.PrBusinessTelephoneNumber,
	}
}

func (s *Server) handlePutProfile(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	// Updates are often partial (e.g. timezone only), so persist only the directory
	// fields actually present — an absent field is left untouched, never cleared.
	var prof struct {
		DisplayName *string `json:"display_name"`
		Title       *string `json:"title"`
		Department  *string `json:"department"`
		Phone       *string `json:"phone"`
	}
	if err := decodeJSON(r, &prof); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	props := map[uint32]string{}
	if prof.DisplayName != nil {
		props[uint32(mapi.PrDisplayName)] = *prof.DisplayName
	}
	if prof.Title != nil {
		props[uint32(mapi.PrTitle)] = *prof.Title
	}
	if prof.Department != nil {
		props[uint32(mapi.PrDepartmentName)] = *prof.Department
	}
	if prof.Phone != nil {
		props[uint32(mapi.PrBusinessTelephoneNumber)] = *prof.Phone
	}
	if len(props) > 0 {
		if setter, ok := s.auth.(interface {
			SetUserProperties(string, map[uint32]string) (bool, error)
		}); ok {
			_, _ = setter.SetUserProperties(c.Email, props)
		}
	}
	// Return the re-read profile so the SPA reflects the persisted directory state.
	s.handleGetProfile(w, r)
}

// ---- Mailboxes ----

func (s *Server) handleGetMailboxes(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	// The SPA lists these as the user's custom folders in the sidebar, so return
	// only user-created folders (id at/above the unassigned start); the built-in
	// folders are rendered from the SPA's own fixed navigation.
	names := []string{}
	if folders, err := st.ListFolders(); err == nil {
		for _, f := range folders {
			if f.ID >= int64(mapi.PrivateFIDUnassignedStart) {
				names = append(names, f.DisplayName)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailboxes": names})
}

func (s *Server) handleGetSharedMailboxes(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	// Only the shared mailboxes the caller may actually open are returned (store
	// owner / folder grantee / delegate), each as the SPA's SharedMailbox object.
	boxes := []map[string]any{}
	if lister, ok := s.auth.(directory.SharedMailboxLister); ok {
		if list, err := lister.SharedMailboxes(); err == nil {
			for _, b := range list {
				st, err := objectstore.Open(b.StorePath)
				if err != nil {
					continue
				}
				if callerMayOpenShared(st, c.Email) {
					boxes = append(boxes, map[string]any{
						"owner":       b.Address,
						"mailbox":     b.Address,
						"displayName": b.Address,
						"rights":      "read",
					})
				}
				st.Close()
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"shared_mailboxes": boxes})
}

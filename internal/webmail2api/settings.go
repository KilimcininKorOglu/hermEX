package webmail2api

import (
	"encoding/json"
	"net/http"
	"strings"

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

// ---- Safe senders (remote-content allowlist, shared with internal/webmail under
// the same "safeSenders" blob key so both clients honor one list) ----

// safeSenders reads the shared safe-sender allowlist from the settings blob.
func safeSenders(st *objectstore.Store) []string {
	list := []string{}
	if raw, ok := sharedSettings(st)["safeSenders"]; ok {
		_ = json.Unmarshal(raw, &list)
	}
	return list
}

// normalizeSafeSenders lowercases, trims, and de-duplicates a safe-sender list,
// dropping empties, matching the server-rendered webmail so both clients store
// the list the same way.
func normalizeSafeSenders(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// isSafeSender reports whether a parsed sender address is covered by the
// allowlist: an exact address match, or a domain entry (bare or "@domain")
// matching the sender's domain. Case-insensitive on the address only; no
// subdomain widening. Mirrors internal/webmail's matching.
func isSafeSender(list []string, addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return false
	}
	domain := ""
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		domain = addr[i+1:]
	}
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if e == addr || (domain != "" && (e == domain || e == "@"+domain)) {
			return true
		}
	}
	return false
}

func (s *Server) handleGetSafeSenders(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		list := []string{}
		if raw, ok := m["safeSenders"]; ok {
			_ = json.Unmarshal(raw, &list)
		}
		return map[string]any{"safeSenders": list}, false
	})
}

func (s *Server) handlePutSafeSenders(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SafeSenders []string `json:"safeSenders"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	list := normalizeSafeSenders(body.SafeSenders)
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		raw, _ := json.Marshal(list)
		m["safeSenders"] = raw
		return map[string]any{"safeSenders": list}, true
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

// userLocaleReader / userLocaleWriter are the narrow directory capabilities
// webmail2 uses to read and persist the caller's OWN timezone + language locale
// (the users.timezone / users.lang columns). SQLDirectory satisfies both; when a
// capability is absent the locale degrades to empty (follow-the-device) instead
// of failing.
type userLocaleReader interface {
	GetUser(string) (directory.UserDetail, bool, error)
}

type userLocaleWriter interface {
	SetUserLocale(username, timezone, lang string) (bool, error)
}

// userLocale returns the caller's persisted timezone + language locale from the
// directory, both empty when the capability or the user is unavailable.
func (s *Server) userLocale(email string) (timezone, locale string) {
	if rd, ok := s.auth.(userLocaleReader); ok {
		if u, found, err := rd.GetUser(email); err == nil && found {
			return u.Timezone, u.Lang
		}
	}
	return "", ""
}

// onboardedFlag reports whether the caller has completed first-run onboarding.
// The flag lives in the shared webmail settings blob; an absent flag means a
// fresh account that has not onboarded yet, so the onboarding gate still fires.
func onboardedFlag(st *objectstore.Store) bool {
	m := sharedSettings(st)
	if raw, ok := m["onboarded"]; ok {
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			return b
		}
	}
	return false
}

func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	tz, locale := s.userLocale(c.Email)
	prof := map[string]any{"email": c.Email, "timezone": tz, "locale": locale}
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
	// Storage usage + onboarding flag from the store; quota limits are best-effort
	// (0 = unlimited), and onboarded defaults false for a fresh account.
	if st, err := objectstore.Open(c.Mailbox); err == nil {
		if used, err := st.MailboxSize(); err == nil {
			prof["quota_used"] = used
		}
		prof["onboarded"] = onboardedFlag(st)
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
		Timezone    *string `json:"timezone"`
		Locale      *string `json:"locale"`
		Onboarded   *bool   `json:"onboarded"`
		// theme is presentation-only and client-persisted (cookie); the server
		// intentionally does not store it, so it is not decoded here.
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
	// Timezone + locale persist to the directory (users.timezone / users.lang) so
	// they survive a reload and are available cross-protocol. Read-merge-write the
	// pair: an absent field keeps its current value rather than clearing it.
	if prof.Timezone != nil || prof.Locale != nil {
		tz, locale := s.userLocale(c.Email)
		if prof.Timezone != nil {
			tz = *prof.Timezone
		}
		if prof.Locale != nil {
			locale = *prof.Locale
		}
		if wr, ok := s.auth.(userLocaleWriter); ok {
			_, _ = wr.SetUserLocale(c.Email, tz, locale)
		}
	}
	// The onboarded flag lives in the shared webmail settings blob (per-mailbox
	// store), set true when the user finishes the first-run onboarding step.
	if prof.Onboarded != nil {
		if st, err := objectstore.Open(c.Mailbox); err == nil {
			m := sharedSettings(st)
			if b, err := json.Marshal(*prof.Onboarded); err == nil {
				m["onboarded"] = b
				_ = saveSharedSettings(st, m)
			}
			st.Close()
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

// handleGetSharedAsOwner lists the shared mailboxes the caller OWNS (is a store
// owner of): the "share-out" view, the counterpart to /mailboxes/shared, which
// lists mailboxes shared TO the caller. It returns bare addresses under the
// shared_as_owner key, the shape the SPA reads. This path previously reused
// handleGetSharedMailboxes and so returned the wrong key with the shared-to-me
// data. Store paths are server-derived from the directory (never the request),
// so a forged caller cannot probe an arbitrary store.
func (s *Server) handleGetSharedAsOwner(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	owned := []string{}
	if lister, ok := s.auth.(directory.SharedMailboxLister); ok {
		if list, err := lister.SharedMailboxes(); err == nil {
			for _, b := range list {
				st, err := objectstore.Open(b.StorePath)
				if err != nil {
					continue
				}
				if owner, err := st.IsStoreOwner(c.Email); err == nil && owner {
					owned = append(owned, b.Address)
				}
				st.Close()
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"shared_as_owner": owned})
}

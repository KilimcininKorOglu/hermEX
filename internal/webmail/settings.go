package webmail

import (
	"encoding/json"
	"net/http"
	"strings"

	"hermex/internal/objectstore"
)

// settingsSchemaVersion is stamped into stored settings for cheap forward
// compatibility.
const settingsSchemaVersion = 1

// webmailSettings holds a user's webmail preferences. It is persisted as a
// single JSON blob in a store-root MAPI property (objectstore.GetWebmailSettings),
// so preferences live as a property rather than in a dedicated table. The schema
// is original to this project.
type webmailSettings struct {
	SchemaVersion         int         `json:"schemaVersion"`
	ComposeFormat         string      `json:"composeFormat"` // "html" | "plain"
	Signatures            []signature `json:"signatures"`
	DefaultSignatureNew   string      `json:"defaultSignatureNew"`   // signature id for new messages, or ""
	DefaultSignatureReply string      `json:"defaultSignatureReply"` // signature id for replies/forwards, or ""
	Density               string      `json:"density"`               // message-list row density: "compact" | "extended"
	DefaultSort           string      `json:"defaultSort"`           // default list sort key when no URL param ("" → date)
	DefaultDir            string      `json:"defaultDir"`            // default list sort direction when no URL param ("" → desc)
	Categories            []category  `json:"categories"`            // master colored-category list (assigned to messages as PidNameKeywords)
	PreviewPane           string      `json:"previewPane"`           // reading-pane location: "none" | "right" | "bottom"
}

// category is one named, colored label in the mailbox's master category list.
// A message carries category names (PidNameKeywords); the color is resolved from
// this list for display.
type category struct {
	Name  string `json:"name"`
	Color string `json:"color"` // CSS color, e.g. "#b00020"
}

// mailboxCategories returns a mailbox's master category list, or nil on error —
// used by the per-row icon enrichment to color category badges.
func mailboxCategories(st *objectstore.Store) []category {
	cfg, err := loadSettings(st)
	if err != nil {
		return nil
	}
	return cfg.Categories
}

// catColor returns the color for a category name from a master list, or a
// neutral grey when the name is not present (assigned but since removed).
func catColor(cats []category, name string) string {
	for _, c := range cats {
		if c.Name == name {
			return c.Color
		}
	}
	return "#6b7280"
}

// signature is one named signature. HTML holds the signature markup when IsHTML
// is true, or plain text otherwise.
type signature struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	HTML   string `json:"html"`
	IsHTML bool   `json:"isHTML"`
}

// signatureByID returns the signature with the given id, or false when the id is
// empty or no longer matches a stored signature (a dangling default reference).
func (s webmailSettings) signatureByID(id string) (signature, bool) {
	if id == "" {
		return signature{}, false
	}
	for _, sig := range s.Signatures {
		if sig.ID == id {
			return sig, true
		}
	}
	return signature{}, false
}

// defaultSettings is what a mailbox uses until it saves its own preferences.
func defaultSettings() webmailSettings {
	return webmailSettings{
		SchemaVersion: settingsSchemaVersion, ComposeFormat: "html", Density: "compact", DefaultSort: "date", DefaultDir: "desc", PreviewPane: "none",
		Categories: []category{
			{Name: "Red", Color: "#b00020"},
			{Name: "Orange", Color: "#e67e22"},
			{Name: "Green", Color: "#27ae60"},
			{Name: "Blue", Color: "#2563eb"},
			{Name: "Purple", Color: "#8e44ad"},
		},
	}
}

// loadSettings reads and decodes a mailbox's webmail settings, returning the
// defaults when none have been stored yet.
func loadSettings(st *objectstore.Store) (webmailSettings, error) {
	raw, err := st.GetWebmailSettings()
	if err != nil {
		return webmailSettings{}, err
	}
	if raw == "" {
		return defaultSettings(), nil
	}
	var s webmailSettings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return webmailSettings{}, err
	}
	if s.ComposeFormat == "" {
		s.ComposeFormat = "html"
	}
	if s.Density == "" {
		s.Density = "compact"
	}
	if s.DefaultSort == "" {
		s.DefaultSort = "date"
	}
	if s.DefaultDir == "" {
		s.DefaultDir = "desc"
	}
	if s.PreviewPane == "" {
		s.PreviewPane = "none"
	}
	return s, nil
}

// saveSettings encodes and stores a mailbox's webmail settings, stamping the
// current schema version.
func saveSettings(st *objectstore.Store, s webmailSettings) error {
	s.SchemaVersion = settingsSchemaVersion
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.SetWebmailSettings(string(b))
}

// handleSettingsForm renders the settings page: the default compose format and
// the signature list, with assignments for new messages and replies/forwards.
func (s *Server) handleSettingsForm(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()
	cfg, err := loadSettings(st)
	if err != nil {
		cfg = defaultSettings()
	}
	s.render(w, "settings", cfg)
}

// handleSettingsSubmit applies one settings action — saving preferences, adding
// a signature, or deleting one — then redirects back to the settings page.
func (s *Server) handleSettingsSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()
	cfg, err := loadSettings(st)
	if err != nil {
		cfg = defaultSettings()
	}

	switch r.FormValue("action") {
	case "addsig":
		if name := strings.TrimSpace(r.FormValue("signame")); name != "" {
			sig := signature{ID: randomToken()[:12], Name: name}
			if html := strings.TrimSpace(r.FormValue("sigbodyhtml")); html != "" {
				sig.HTML, sig.IsHTML = html, true
			} else {
				sig.HTML = r.FormValue("sigbody")
			}
			cfg.Signatures = append(cfg.Signatures, sig)
		}
	case "delsig":
		id := r.FormValue("sigid")
		cfg.Signatures = removeSignature(cfg.Signatures, id)
		if cfg.DefaultSignatureNew == id {
			cfg.DefaultSignatureNew = ""
		}
		if cfg.DefaultSignatureReply == id {
			cfg.DefaultSignatureReply = ""
		}
	case "addcat":
		if name := strings.TrimSpace(r.FormValue("catname")); name != "" && !categoryExists(cfg.Categories, name) {
			color := r.FormValue("catcolor")
			if color == "" {
				color = "#6b7280"
			}
			cfg.Categories = append(cfg.Categories, category{Name: name, Color: color})
		}
	case "delcat":
		cfg.Categories = removeCategory(cfg.Categories, r.FormValue("catname"))
	default: // save preferences
		if f := r.FormValue("composeformat"); f == "plain" || f == "html" {
			cfg.ComposeFormat = f
		}
		if d := r.FormValue("density"); d == "compact" || d == "extended" {
			cfg.Density = d
		}
		if p := r.FormValue("previewpane"); p == "none" || p == "right" || p == "bottom" {
			cfg.PreviewPane = p
		}
		// The default sort order is posted as one "key dir" value (e.g. "date desc").
		if parts := strings.Fields(r.FormValue("defaultsort")); len(parts) == 2 {
			cfg.DefaultSort = whitelist(parts[0], "date", "from", "subject", "size", "flag", "read")
			cfg.DefaultDir = whitelist(parts[1], "desc", "asc")
		}
		cfg.DefaultSignatureNew = r.FormValue("defaultnew")
		cfg.DefaultSignatureReply = r.FormValue("defaultreply")
	}

	if err := saveSettings(st, cfg); err != nil {
		http.Error(w, "cannot save settings", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// categoryExists reports whether a category name is already in the master list.
func categoryExists(cats []category, name string) bool {
	for _, c := range cats {
		if c.Name == name {
			return true
		}
	}
	return false
}

// removeCategory returns cats without the category whose name matches.
func removeCategory(cats []category, name string) []category {
	out := make([]category, 0, len(cats))
	for _, c := range cats {
		if c.Name != name {
			out = append(out, c)
		}
	}
	return out
}

// removeSignature returns sigs without the signature whose id matches.
func removeSignature(sigs []signature, id string) []signature {
	out := make([]signature, 0, len(sigs))
	for _, sig := range sigs {
		if sig.ID != id {
			out = append(out, sig)
		}
	}
	return out
}

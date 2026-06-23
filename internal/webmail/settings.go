package webmail

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// mailboxUsageOf computes a mailbox's storage usage and quota for the settings
// widget, best-effort: an unreadable size yields the zero value, and a quota of 0
// (unset) reads as unlimited.
func mailboxUsageOf(st *objectstore.Store) mailboxUsage {
	used, err := st.MailboxSize()
	if err != nil {
		return mailboxUsage{}
	}
	u := mailboxUsage{Used: formatBytes(used)}
	if q, err := st.GetQuota(); err == nil && q.StorageKB > 0 {
		quotaBytes := int64(q.StorageKB) * 1024
		u.Limited = true
		u.Quota = formatBytes(quotaBytes)
		u.Percent = int(min(used*100/quotaBytes, 100))
	}
	return u
}

// formatBytes renders a byte count as a human-readable size (B, KB, MB, ...).
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

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
	IncomingRender        string      `json:"incomingRender"`        // how received mail is displayed: "html" | "plain" (force plain text)
	RequestReceiptDefault bool        `json:"requestReceiptDefault"` // pre-check "request read receipt" on a fresh compose
	SafeSenders           []string    `json:"safeSenders"`           // addresses/domains allowed to load remote content in the reader
	ConversationView      bool        `json:"conversationView"`      // group the message list into RFC 5256 conversation threads
}

// settingsView augments the stored webmail preferences with the user's
// directory-backed allow/block rules for rendering. The embedded webmailSettings
// promotes its fields, so the settings template's existing references resolve
// unchanged.
type settingsView struct {
	webmailSettings
	AccessEnabled bool                      // the rule store is wired, so show the allow/block section
	AccessRules   []directory.RecipientRule // the user's personal allow/block rules
}

// settingsPage is the data for the unified settings page: every settings section
// rendered as a tab on one page. ActiveTab selects which tab opens; ChgPasswd
// gates the Password tab. Each section carries its existing per-section view data
// so the tab partials render unchanged.
type settingsPage struct {
	ActiveTab string
	ChgPasswd bool
	General   settingsView
	Account   accountInfo
	Usage     mailboxUsage
	Rules     rulesPage
	OOF       oofPage
	Smime     smimePage
	Password  passwordPage
}

// accountInfo is the signed-in account's identity for the settings "Account"
// widget: the login and any other addresses it may send as.
type accountInfo struct {
	Email           string
	OtherIdentities []string // send-as addresses other than the primary login
}

// mailboxUsage is the storage a mailbox occupies, with its quota when one is set,
// for the settings "Mailbox usage" widget.
type mailboxUsage struct {
	Used    string // human-readable used size, e.g. "12.3 MB"
	Quota   string // human-readable quota; "" when unlimited
	Percent int    // 0..100 of the quota; 0 when unlimited
	Limited bool   // a storage quota is set
}

// settingsTab normalizes a requested tab to a known one, defaulting to General
// for an empty or unrecognized value.
func settingsTab(raw string) string {
	switch raw {
	case "general", "rules", "oof", "smime", "password":
		return raw
	}
	return "general"
}

// buildSettingsPage assembles the whole settings page from a mailbox store and
// session. Each section loads independently and best-effort: a failure in one
// degrades that section to its empty/default state rather than blanking the page.
func (s *Server) buildSettingsPage(sess *session, st *objectstore.Store, active string) settingsPage {
	page := settingsPage{ActiveTab: settingsTab(active)}

	cfg, err := loadSettings(st)
	if err != nil {
		cfg = defaultSettings()
	}
	page.General = settingsView{webmailSettings: cfg, AccessEnabled: s.Rules != nil}
	if s.Rules != nil {
		page.General.AccessRules, _ = s.Rules.ListRecipientRules(sess.user) // best-effort; an error just shows no rules
	}
	acct := accountInfo{Email: sess.user}
	for _, id := range s.identities(sess.user) {
		if !strings.EqualFold(id, sess.user) {
			acct.OtherIdentities = append(acct.OtherIdentities, id)
		}
	}
	page.Account = acct
	page.Usage = mailboxUsageOf(st)

	page.Rules = s.buildRulesPage(st, sess)
	page.OOF = buildOOFPage(st)
	page.Smime = s.buildSmimePage(st, sess)

	// The Password tab is offered only when the account may change its password
	// here — has the privilege and is local, not LDAP/AD-backed — matching the
	// submit handler's own gate.
	page.ChgPasswd = s.passwordChangeAllowed(sess.user)
	return page
}

// applySettingsNotices expands the redirect flags a section posts back with into
// its per-section notice fields, scoped to the active tab — so the right panel
// shows its result and the codes (e.g. password "current") survive the 303.
func applySettingsNotices(page *settingsPage, q url.Values) {
	switch page.ActiveTab {
	case "oof":
		page.OOF.Saved = q.Get("saved") == "1"
	case "password":
		page.Password.Saved = q.Get("saved") == "1"
		page.Password.Error = passwordError(q.Get("err"))
	case "rules":
		page.Rules.Err = errNotice(q.Get("err"))
		if q.Get("ran") == "1" {
			page.Rules.Ran = true
			page.Rules.Affected, _ = strconv.Atoi(q.Get("affected"))
			page.Rules.Evaluated, _ = strconv.Atoi(q.Get("evaluated"))
		}
	case "smime":
		page.Smime.Notice = smimeNotice(q.Get("ok"))
	}
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
		SchemaVersion: settingsSchemaVersion, ComposeFormat: "html", Density: "compact", DefaultSort: "date", DefaultDir: "desc", PreviewPane: "none", IncomingRender: "html",
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
	if s.IncomingRender == "" {
		s.IncomingRender = "html"
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

// handleSettingsForm renders the unified settings page — every section (general
// preferences, inbox rules, out of office, certificates, password) as a tab on
// one page, with the requested tab open and any post-redirect notice expanded.
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
	q := r.URL.Query()
	page := s.buildSettingsPage(sess, st, q.Get("tab"))
	applySettingsNotices(&page, q)
	s.render(w, "settings", page)
}

// handleSettingsSubmit applies one settings action — saving preferences, adding
// a signature, or deleting one — then redirects back to the settings page.
func (s *Server) handleSettingsSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	// Allow/block rules are directory-backed (the MTA reads them at delivery), not part
	// of the objectstore-stored preferences, so they are applied here without opening
	// the mailbox. Invalid input is ignored, matching the other add actions.
	switch r.FormValue("action") {
	case "addrule":
		if s.Rules != nil {
			_ = s.Rules.SetRecipientRule(sess.user, r.FormValue("pattern"), r.FormValue("ruleaction"))
		}
		http.Redirect(w, r, "/settings?tab=general", http.StatusSeeOther)
		return
	case "delrule":
		if s.Rules != nil {
			_, _ = s.Rules.DeleteRecipientRule(sess.user, r.FormValue("pattern"))
		}
		http.Redirect(w, r, "/settings?tab=general", http.StatusSeeOther)
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
	case "addsafe":
		if e := normalizeSafeSender(r.FormValue("safesender")); e != "" && !containsStr(cfg.SafeSenders, e) {
			cfg.SafeSenders = append(cfg.SafeSenders, e)
		}
	case "delsafe":
		cfg.SafeSenders = removeStr(cfg.SafeSenders, normalizeSafeSender(r.FormValue("safesender")))
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
		if v := r.FormValue("incomingrender"); v == "html" || v == "plain" {
			cfg.IncomingRender = v
		}
		// A checkbox posts a value only when checked; its absence on a full "save"
		// submit therefore clears the preference.
		cfg.RequestReceiptDefault = r.FormValue("requestreceipt") != ""
		cfg.ConversationView = r.FormValue("conversationview") != ""
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
	http.Redirect(w, r, "/settings?tab=general", http.StatusSeeOther)
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

// normalizeSafeSender trims and lowercases a safe-sender entry for storage and
// comparison; an entry is a full address ("bob@example.com") or a domain
// ("example.com" or "@example.com").
func normalizeSafeSender(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// removeStr returns list without the entries equal to v.
func removeStr(list []string, v string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s != v {
			out = append(out, s)
		}
	}
	return out
}

// isSafeSender reports whether a parsed sender address is covered by the
// safe-senders list — an exact address match, or a domain entry matching the
// sender's domain. Matching is case-insensitive on the parsed address only
// (never the display name) and does not extend to subdomains.
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
		if e == addr {
			return true
		}
		if domain != "" && (e == domain || e == "@"+domain) {
			return true
		}
	}
	return false
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

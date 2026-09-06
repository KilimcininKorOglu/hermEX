package webmail2api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// sharedSettings reads the shared webmail settings blob (PrWebmailSettings) as a
// generic object so webmail2 can update only the keys it owns without dropping
// fields the server-rendered webmail manages (composeFormat, density, ...). Both
// clients share categories and signatures this way, the user's real settings,
// not a per-client fork.
// It reports ok=false when the stored blob could not be read or parsed, which is
// NOT the same as a mailbox that has none. Both used to answer with an empty
// map, and a write then put that empty map back, so one unreadable read cost the
// user every favourite, category, signature and display setting they had, and
// the write reported success.
func sharedSettings(st *objectstore.Store) (m map[string]json.RawMessage, ok bool) {
	m = map[string]json.RawMessage{}
	props, err := st.GetStoreProperties(mapi.PrWebmailSettings)
	if err != nil {
		st.LogSwallowedError("settings.read", err)
		return nil, false
	}
	v, has := props.Get(mapi.PrWebmailSettings)
	if !has {
		return m, true // a mailbox that has never stored settings
	}
	str, isStr := v.(string)
	if !isStr {
		st.LogSwallowedError("settings.read", fmt.Errorf("stored settings are %T, not a string", v))
		return nil, false
	}
	if str == "" {
		return m, true
	}
	if err := json.Unmarshal([]byte(str), &m); err != nil {
		st.LogSwallowedError("settings.parse", err)
		return nil, false
	}
	return m, true
}

// settingsForWrite loads the blob for a read-modify-write. It refuses when the
// stored blob is unreadable, because writing back what was assembled from
// nothing replaces the user's settings with only the key being set.
func settingsForWrite(st *objectstore.Store) (map[string]json.RawMessage, bool) {
	return sharedSettings(st)
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

// lockSettings acquires the per-mailbox settings lock and returns its unlock. The
// lock spans the read-modify-write in withSettings, so concurrent PUTs to one
// mailbox's settings blob are serialized rather than racing on a shared read.
func (s *Server) lockSettings(mailbox string) func() {
	mu, _ := s.settingsLocks.LoadOrStore(mailbox, &sync.Mutex{})
	m := mu.(*sync.Mutex)
	m.Lock()
	return m.Unlock
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
	// Serialize the whole read-modify-write per mailbox, so a concurrent PUT on a
	// different settings key cannot read the same starting blob and overwrite this
	// change (or have this one overwrite it).
	unlock := s.lockSettings(c.Mailbox)
	defer unlock()
	m, loaded := sharedSettings(st)
	if !loaded {
		// Reading failed, so what is stored is unknown. Handing an empty map to a
		// handler that saves would write the user's settings away.
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "settings unavailable"})
		return
	}
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

// ---- Calendar settings (per-user, shared webmail settings blob) ----

// appearanceSettingsJSON is the persisted display/appearance settings: theme,
// language, date/time format, name display order, and the unread/widget-panel
// toggles. Zero values normalize to the Exchange defaults (system theme,
// browser language, ISO dates, 24h, first-then-last name).
type appearanceSettingsJSON struct {
	Theme             string `json:"theme"`       // "light" | "dark" | "system"
	Language          string `json:"language"`    // "en" | "tr" | "system"
	DateFormat        string `json:"dateFormat"`  // "iso" | "dmy" | "mdy"
	TimeFormat        string `json:"timeFormat"`  // "12" | "24"
	NameDisplay       string `json:"nameDisplay"` // "firstlast" | "lastfirst"
	ShowUnreadCounter bool   `json:"showUnreadCounter"`
	UnreadBorder      bool   `json:"unreadBorder"`
	HideWidgetPanel   bool   `json:"hideWidgetPanel"`
	StartupFolder     string `json:"startupFolder"` // "" or a known folder slug like "inbox"/"today"/"calendar"
	AutoCc            string `json:"autoCc"`        // comma-separated addresses auto-added to Cc on every send
	ShortcutMode      string `json:"shortcutMode"`  // "off" | "basic" | "extended" keyboard-shortcut level
	IconSet           string `json:"iconSet"`       // "breeze" | "classic" icon style
	ShowItemData      bool   `json:"showItemData"`  // developer tool: show the raw item-data button on a message
	FilePreview       bool   `json:"filePreview"`   // inline attachment preview (PDF + images) on a message
	PdfZoom           string `json:"pdfZoom"`       // "auto" | "page-actual" | "page-width" inline-PDF zoom
	InboxNavMode      string `json:"inboxNavMode"`  // "pagination" | "infinite" message-list navigation
	InboxPageSize     int    `json:"inboxPageSize"` // rows per page/block (10..200)

	MailListColumns *mailListColumnsJSON `json:"mailListColumns"` // per-user message-list column visibility
}

// inboxPageSize bounds match the reference SettingsInboxNavigationWidget
// (10..200 rows per page/block); the default window is 50.
const (
	minInboxPageSize     = 10
	maxInboxPageSize     = 200
	defaultInboxPageSize = 50
)

// mailListColumnsJSON is the message-list column visibility (reference
// MailGridColumnModel): the sender/subject/date columns are always shown, so
// only the optional columns are toggleable. A nil struct means "all defaults".
type mailListColumnsJSON struct {
	Preview    bool `json:"preview"`    // subject-line snippet (", preview text")
	Attachment bool `json:"attachment"` // paperclip icon when the message has attachments
	Importance bool `json:"importance"` // high/low importance marker
	Categories bool `json:"categories"` // category/label badges
	Size       bool `json:"size"`       // message size column
	Flag       bool `json:"flag"`       // follow-up flag marker
}

// defaultMailListColumns is the initial column visibility: the reference shows
// From/Subject/Received always, an attachment + importance icon, and category
// badges; size and the follow-up flag stay hidden until the user opts in.
func defaultMailListColumns() mailListColumnsJSON {
	return mailListColumnsJSON{Preview: true, Attachment: true, Importance: true, Categories: true, Size: false, Flag: false}
}

// readAppearanceSettings loads the display settings from the shared blob,
// defaulting to system theme, browser language, ISO date, 24h time, and a
// first-then-last name order when none are stored.
func readAppearanceSettings(m map[string]json.RawMessage) appearanceSettingsJSON {
	a := appearanceSettingsJSON{Theme: "system", Language: "system", DateFormat: "iso", TimeFormat: "24", NameDisplay: "firstlast", ShortcutMode: "extended", IconSet: "breeze", FilePreview: true, PdfZoom: "page-width", InboxNavMode: "pagination", InboxPageSize: defaultInboxPageSize}
	if raw, ok := m["webmail2AppearanceSettings"]; ok {
		_ = json.Unmarshal(raw, &a)
	}
	// Every stored value is client-supplied, so each is narrowed back to the set
	// the SPA renders; anything else takes the default rather than reaching the UI.
	a.Theme = oneOf(a.Theme, "system", "light", "dark", "system")
	a.Language = oneOf(a.Language, "system", "en", "tr", "system")
	a.DateFormat = oneOf(a.DateFormat, "iso", "iso", "dmy", "mdy")
	a.TimeFormat = oneOf(a.TimeFormat, "24", "12", "24")
	a.NameDisplay = oneOf(a.NameDisplay, "firstlast", "firstlast", "lastfirst")
	a.ShortcutMode = oneOf(a.ShortcutMode, "extended", "off", "basic", "extended")
	a.IconSet = oneOf(a.IconSet, "breeze", "breeze", "classic")
	a.PdfZoom = oneOf(a.PdfZoom, "page-width", "auto", "page-actual", "page-width")
	a.InboxNavMode = oneOf(a.InboxNavMode, "pagination", "infinite", "pagination")
	a.InboxPageSize = clampInboxPageSize(a.InboxPageSize)
	// A never-configured message-list gets the default column set; once stored,
	// the persisted per-column booleans are honored verbatim.
	if a.MailListColumns == nil {
		d := defaultMailListColumns()
		a.MailListColumns = &d
	}
	return a
}

// oneOf narrows a stored string to the values the SPA renders, falling back
// when it carries anything else.
func oneOf(v, fallback string, allowed ...string) string {
	if slices.Contains(allowed, v) {
		return v
	}
	return fallback
}

// clampInboxPageSize takes the default for a never-configured (zero) page size
// and otherwise clamps to the reference row-count bounds.
func clampInboxPageSize(n int) int {
	if n == 0 {
		return defaultInboxPageSize
	}
	return min(max(n, minInboxPageSize), maxInboxPageSize)
}

// appearanceResponseJSON is the appearance settings plus the operator's inline
// preview cap. The cap is NOT a user setting and is never read back from a PUT: it
// rides here because this is where the client already learns whether to preview at
// all, so it costs no extra request when a message opens.
type appearanceResponseJSON struct {
	appearanceSettingsJSON
	PreviewMaxBytes int64 `json:"previewMaxBytes"`
}

// handleGetAppearanceSettings returns the user's display/appearance settings.
func (s *Server) handleGetAppearanceSettings(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		return appearanceResponseJSON{
			appearanceSettingsJSON: readAppearanceSettings(m),
			PreviewMaxBytes:        maxPreviewBytes(),
		}, false
	})
}

// handlePutAppearanceSettings persists the display/appearance settings, clamping
// each enum to its valid value.
func (s *Server) handlePutAppearanceSettings(w http.ResponseWriter, r *http.Request) {
	var in appearanceSettingsJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		// Carry the stored defaults, then overlay the input and re-clamp each enum
		// so a bad value still stores a valid one.
		a := readAppearanceSettings(m)
		a.Theme = in.Theme
		a.Language = in.Language
		a.DateFormat = in.DateFormat
		a.TimeFormat = in.TimeFormat
		a.NameDisplay = in.NameDisplay
		a.ShowUnreadCounter = in.ShowUnreadCounter
		a.UnreadBorder = in.UnreadBorder
		a.HideWidgetPanel = in.HideWidgetPanel
		a.StartupFolder = in.StartupFolder
		a.AutoCc = in.AutoCc
		a.ShortcutMode = in.ShortcutMode
		a.IconSet = in.IconSet
		a.ShowItemData = in.ShowItemData
		a.FilePreview = in.FilePreview
		a.PdfZoom = in.PdfZoom
		a.InboxNavMode = in.InboxNavMode
		a.InboxPageSize = in.InboxPageSize
		// The message-list columns are stored as sent (each a plain bool); a nil
		// input keeps the stored/default set rather than clearing every column.
		if in.MailListColumns != nil {
			a.MailListColumns = in.MailListColumns
		}
		clamped := readAppearanceSettings(map[string]json.RawMessage{"webmail2AppearanceSettings": mustJSON(a)})
		raw, _ := json.Marshal(clamped)
		m["webmail2AppearanceSettings"] = raw
		// The stored blob holds the user's settings only; the operator's preview cap
		// rides back on the response so a save does not leave the client without it.
		return appearanceResponseJSON{appearanceSettingsJSON: clamped, PreviewMaxBytes: maxPreviewBytes()}, true
	})
}

// setOnboardedFlag records that the caller finished first-run onboarding. It
// leaves the blob alone when it could not be read, because writing back what was
// assembled from an unreadable one replaces the user's settings.
func setOnboardedFlag(mailbox string, done bool) {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return
	}
	defer st.Close()
	m, loaded := settingsForWrite(st)
	if !loaded {
		return
	}
	b, err := json.Marshal(done)
	if err != nil {
		return
	}
	m["onboarded"] = b
	if err := saveSharedSettings(st, m); err != nil {
		st.LogSwallowedError("settings.onboarded", err)
	}
}

// mustJSON marshals v, returning nil on error (the input is a controlled struct).
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// handleResetSettings drops every webmail2-owned settings key (appearance,
// calendar, preferences) from the shared blob, leaving keys the server-rendered
// webmail owns untouched. The next GET repopulates from defaults.
func (s *Server) handleResetSettings(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		for k := range m {
			if strings.HasPrefix(k, "webmail2") {
				delete(m, k)
			}
		}
		return map[string]bool{"ok": true}, true
	})
}

// calendarSettingsJSON is the persisted calendar display settings. FirstDayOfWeek
// is the week-start day (0=Sun..6=Sat); Resolution is the time-grid slot minutes
// (5/10/15/30/60); WorkDayStart/End are the working-hours bounds (hour 0-23);
// ShowNonWorkingHours controls whether the time grid renders hours outside the
// working window or hides them. Zero values normalize to the Exchange defaults
// (Monday, 30min, 09:00-18:00, show all hours) so a fresh account lands on a sane grid.
type calendarSettingsJSON struct {
	FirstDayOfWeek      int   `json:"firstDayOfWeek"`
	Resolution          int   `json:"resolution"`
	WorkDayStart        int   `json:"workDayStart"`
	WorkDayEnd          int   `json:"workDayEnd"`
	ShowNonWorkingHours bool  `json:"showNonWorkingHours"`
	WorkDays            []int `json:"workDays"`        // 0=Sun..6=Sat that are working days (default Mon-Fri [1,2,3,4,5])
	DefaultDuration     int   `json:"defaultDuration"` // default appointment length in minutes (default 30)
	DefaultReminder     int   `json:"defaultReminder"` // default reminder minutes before start (default 15; 0 = none)
}

// readCalendarSettings loads the calendar display settings from the shared blob,
// defaulting to Monday-first, 30-minute resolution, and 09:00-18:00 working hours
// (showing all hours) when none are stored yet.
func readCalendarSettings(m map[string]json.RawMessage) calendarSettingsJSON {
	cs := calendarSettingsJSON{FirstDayOfWeek: 1, Resolution: 30, WorkDayStart: 9, WorkDayEnd: 18, ShowNonWorkingHours: true, WorkDays: []int{1, 2, 3, 4, 5}, DefaultDuration: 30, DefaultReminder: 15}
	if raw, ok := m["webmail2CalendarSettings"]; ok {
		_ = json.Unmarshal(raw, &cs)
	}
	clampCalendarSettings(&cs)
	return cs
}

// clampCalendarSettings returns every field to its valid range, so a stored blob
// and a fresh PUT are narrowed the same way.
func clampCalendarSettings(cs *calendarSettingsJSON) {
	cs.FirstDayOfWeek = inRange(cs.FirstDayOfWeek, 0, 6, 1)
	cs.WorkDayStart = inRange(cs.WorkDayStart, 0, 23, 9)
	cs.WorkDayEnd = inRange(cs.WorkDayEnd, 0, 23, 18)
	if len(cs.WorkDays) == 0 {
		cs.WorkDays = []int{1, 2, 3, 4, 5}
	}
	if cs.DefaultDuration <= 0 {
		cs.DefaultDuration = 30
	}
	if cs.DefaultReminder < 0 {
		cs.DefaultReminder = 15
	}
	if !validResolution(cs.Resolution) {
		cs.Resolution = 30
	}
}

// inRange takes the default for a value outside [lo,hi]. A stored value is
// client-supplied, so it is replaced rather than clamped to an edge the user
// never chose.
func inRange(v, lo, hi, fallback int) int {
	if v < lo || v > hi {
		return fallback
	}
	return v
}

// validResolution reports whether n is one of the supported time-slot resolutions.
func validResolution(n int) bool {
	switch n {
	case 5, 10, 15, 30, 60:
		return true
	}
	return false
}

// handleGetCalendarSettings returns the user's calendar display settings so the
// SPA can render the month/week grids starting on the configured week-start day
// and at the configured time-slot resolution, shading or hiding non-working hours.
func (s *Server) handleGetCalendarSettings(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		return readCalendarSettings(m), false
	})
}

// handlePutCalendarSettings persists the user's calendar display settings in the
// shared webmail settings blob, clamping each field to its valid range.
func (s *Server) handlePutCalendarSettings(w http.ResponseWriter, r *http.Request) {
	var in calendarSettingsJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	clampCalendarSettings(&in)
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		raw, _ := json.Marshal(in)
		m["webmail2CalendarSettings"] = raw
		return in, true
	})
}

// ---- Categories (shared shape {name,color}) ----

type categoryJSON struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// handleGetCategories serves the mailbox's master category list. It is read from
// the Outlook-compatible configuration message, so webmail and Outlook show the
// same categories; a mailbox that has only the older webmail-private list is
// seeded from it once, on this first read.
func (s *Server) handleGetCategories(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		l, ok, err := readCategoryList(st)
		if err != nil {
			logError("read-category-list", err, logging.Fields{})
			// Fall back to the webmail-private list rather than showing none: a
			// stream this server cannot parse is Outlook's, and the user still has
			// categories on their messages either way.
			cats := []categoryJSON{}
			if raw, hasRaw := m["categories"]; hasRaw {
				_ = json.Unmarshal(raw, &cats)
			}
			return map[string]any{"categories": cats}, false
		}
		if !ok {
			seeded, serr := seedCategoryList(st, m)
			if serr != nil {
				logError("seed-category-list", serr, logging.Fields{})
			}
			l = seeded
		}
		return map[string]any{"categories": categoriesFromList(l)}, false
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
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		// Fold the edit onto the stored list so each surviving category keeps the
		// guid, shortcut and usage counts Outlook owns.
		stored, _, err := readCategoryList(st)
		if err != nil {
			logError("read-category-list", err, logging.Fields{})
		}
		merged := mergeCategories(stored, body.Categories)
		if err := writeCategoryList(st, merged); err != nil {
			logError("write-category-list", err, logging.Fields{})
		}
		// The webmail-private copy is kept in step so a client reading the settings
		// blob directly does not see a stale list.
		raw, _ := json.Marshal(body.Categories)
		m["categories"] = raw
		return map[string]any{"categories": categoriesFromList(merged)}, true
	})
}

// ---- Safe senders (remote-content allowlist, shared with internal/webmail under
// the same "safeSenders" blob key so both clients honor one list) ----

// safeSenders reads the shared safe-sender allowlist from the settings blob.
func safeSenders(st *objectstore.Store) []string {
	list := []string{}
	stored, _ := sharedSettings(st)
	if raw, ok := stored["safeSenders"]; ok {
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
	m, _ := sharedSettings(st)
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
	// properties (keyed by full proptag), the same fields the GAL and Outlook show.
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
		_ = st.Close()
	}
	writeJSON(w, http.StatusOK, prof)
}

// profileProps maps the SPA's editable profile fields to their directory MAPI
// proptags, the cross-protocol properties the GAL and Outlook also read.
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
	// fields actually present, an absent field is left untouched, never cleared.
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
	for _, f := range []struct {
		tag   mapi.PropTag
		value *string
	}{
		{mapi.PrDisplayName, prof.DisplayName},
		{mapi.PrTitle, prof.Title},
		{mapi.PrDepartmentName, prof.Department},
		{mapi.PrBusinessTelephoneNumber, prof.Phone},
	} {
		if f.value != nil {
			props[uint32(f.tag)] = *f.value
		}
	}
	s.writeUserProperties(c.Email, props)
	s.writeUserLocale(c.Email, prof.Timezone, prof.Locale)
	// The onboarded flag lives in the shared webmail settings blob (per-mailbox
	// store), set true when the user finishes the first-run onboarding step.
	if prof.Onboarded != nil {
		setOnboardedFlag(c.Mailbox, *prof.Onboarded)
	}
	// Return the re-read profile so the SPA reflects the persisted directory state.
	s.handleGetProfile(w, r)
}

// writeUserProperties persists the directory-backed profile fields the request
// carried, when the directory supports writing them at all.
func (s *Server) writeUserProperties(email string, props map[uint32]string) {
	if len(props) == 0 {
		return
	}
	setter, ok := s.auth.(interface {
		SetUserProperties(string, map[uint32]string) (bool, error)
	})
	if !ok {
		return
	}
	_, _ = setter.SetUserProperties(email, props)
}

// writeUserLocale persists timezone and locale to the directory
// (users.timezone / users.lang) so they survive a reload and are available
// cross-protocol. The pair is read-merge-written: an absent field keeps its
// current value rather than clearing it.
func (s *Server) writeUserLocale(email string, timezone, locale *string) {
	if timezone == nil && locale == nil {
		return
	}
	wr, ok := s.auth.(userLocaleWriter)
	if !ok {
		return
	}
	tz, loc := s.userLocale(email)
	if timezone != nil {
		tz = *timezone
	}
	if locale != nil {
		loc = *locale
	}
	_, _ = wr.SetUserLocale(email, tz, loc)
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
		if list, err := lister.SharedMailboxes(c.Email); err == nil {
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
				_ = st.Close()
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
		if list, err := lister.SharedMailboxes(c.Email); err == nil {
			for _, b := range list {
				st, err := objectstore.Open(b.StorePath)
				if err != nil {
					continue
				}
				if owner, err := st.IsStoreOwner(c.Email); err == nil && owner {
					owned = append(owned, b.Address)
				}
				_ = st.Close()
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"shared_as_owner": owned})
}

// handleGetIdentities returns the send-as addresses the caller owns directly: the
// primary login plus every alias/altname the directory binds to it. These are the
// "General send-as" identities the compose picker offers beyond shared mailboxes,
// and each is one the send gate (resolveSender) authorizes without a grant. The
// list always includes the caller, so a directory that cannot enumerate aliases
// still yields a usable (self-only) result rather than an empty one.
func (s *Server) handleGetIdentities(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	addrs := []string{c.Email}
	if id, isID := s.accounts.(directory.Identifier); isID {
		if list, err := id.Identities(c.Email); err == nil {
			for _, a := range list {
				a = strings.TrimSpace(a)
				if a == "" || strings.EqualFold(a, c.Email) {
					continue
				}
				addrs = append(addrs, a)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"identities": addrs})
}

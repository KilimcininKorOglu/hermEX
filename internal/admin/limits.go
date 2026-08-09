package admin

import (
	"net/http"

	"hermex/internal/directory"
)

// defaultIMAPLiteralMB and defaultEWSRequestMB mirror each protocol server's built-in
// cap (50 MiB / 8 MiB), shown on the page until an operator saves one. The servers' own
// constants are unexported.
const (
	defaultIMAPLiteralMB       = 50
	defaultEWSRequestMB        = 8
	defaultActiveSyncRequestMB = 4
	defaultDAVICalMB           = 4
	defaultDAVVCardMB          = 4
	defaultWebmailRequestMB    = 40
	defaultMapiRequestMB       = 32
)

// handleUILimits renders the protocol size-limits page (system admins).
func (s *Server) handleUILimits(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "limits.html", s.limitsPageData(r, ""))
}

// limitsPageData builds the size-limits page model: each protocol cap shown in whole
// MB (the stored value, or the built-in default when none has been saved).
func (s *Server) limitsPageData(r *http.Request, notice string) map[string]any {
	data := map[string]any{"Nav": "limits", "Notice": notice, "CSRF": csrfCookieValue(r)}
	s.fillSizeLimits(data)
	s.fillHTTPRateLimit(data)
	s.fillLoginLockout(data)
	s.fillFetchPolicy(data)
	return data
}

// defaultLoginMaxFails, defaultLoginWindow and defaultLoginLockout mirror the login
// limiter's own built-in tuning (five failures in 15 minutes locks out for 15
// minutes), shown on the page until an operator saves one.
const (
	defaultLoginMaxFails = 5
	defaultLoginWindow   = 900
	defaultLoginLockout  = 900
)

// fillLoginLockout sets the failed-login limiter's tunables on a page-data map,
// using the stored values or the limiter's built-in defaults.
func (s *Server) fillLoginLockout(data map[string]any) {
	data["LoginMaxFails"] = defaultLoginMaxFails
	data["LoginWindow"], data["LoginLockout"] = defaultLoginWindow, defaultLoginLockout
	if st, found, err := s.dir.GetLoginLockoutSettings(); err == nil && found {
		data["LoginMaxFails"] = st.MaxFails
		data["LoginWindow"] = st.WindowSeconds
		data["LoginLockout"] = st.LockoutSeconds
	}
}

// handleUISaveLoginLockout persists the failed-login limiter's tuning. Every daemon
// with a login chokepoint applies the change within about a minute, no restart. A
// value below 1 is rejected: a limiter that trips after zero failures, or counts
// within a zero-length window, would lock out every login on the daemon.
func (s *Server) handleUISaveLoginLockout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	fails, window := formInt(r, "login_max_fails"), formInt(r, "login_window")
	lockout := formInt(r, "login_lockout")
	if fails < 1 || window < 1 || lockout < 1 {
		s.render(w, "loginlockout-panel", s.limitsPageData(r, "Failures, window and lockout must each be at least 1; settings not saved."))
		return
	}
	st := directory.LoginLockoutSettings{MaxFails: fails, WindowSeconds: window, LockoutSeconds: lockout}
	if err := s.dir.SetLoginLockoutSettings(st); err != nil {
		s.render(w, "loginlockout-panel", s.limitsPageData(r, s.notice("Could not save the login-lockout settings.", err)))
		return
	}
	s.render(w, "loginlockout-panel", s.limitsPageData(r, "Login-lockout settings saved. Every login daemon applies them within a minute, no restart."))
}

// defaultHTTPRateBurst and defaultHTTPRateWindow mirror the limiter's own built-in
// values (600 requests per 60 s), shown on the page until an operator saves one.
const (
	defaultHTTPRateBurst  = 600
	defaultHTTPRateWindow = 60
)

// fillHTTPRateLimit sets the per-client HTTP request limiter's toggle and tunables on
// a page-data map, using the stored values or the limiter's built-in defaults
// (disabled). Shared by the Limits page and the unified Settings page.
func (s *Server) fillHTTPRateLimit(data map[string]any) {
	data["HTTPRateEnabled"] = false
	data["HTTPRateBurst"], data["HTTPRateWindow"] = defaultHTTPRateBurst, defaultHTTPRateWindow
	if st, found, err := s.dir.GetHTTPRateLimitSettings(); err == nil && found {
		data["HTTPRateEnabled"] = st.Enabled
		data["HTTPRateBurst"] = st.Burst
		data["HTTPRateWindow"] = st.WindowSeconds
	}
}

// handleUISaveHTTPRateLimit persists the per-client HTTP request limiter's settings.
// Every HTTP daemon applies the change within about a minute, no restart. A burst or
// window below 1 is rejected so the limiter is never configured to admit zero requests
// or collapse its window.
func (s *Server) handleUISaveHTTPRateLimit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	burst, window := formInt(r, "http_burst"), formInt(r, "http_window")
	if burst < 1 || window < 1 {
		s.render(w, "http-ratelimit-panel", s.limitsPageData(r, "Burst and window must each be at least 1; settings not saved."))
		return
	}
	st := directory.HTTPRateLimitSettings{
		Enabled:       r.FormValue("enabled") == "1",
		Burst:         burst,
		WindowSeconds: window,
	}
	if err := s.dir.SetHTTPRateLimitSettings(st); err != nil {
		s.render(w, "http-ratelimit-panel", s.limitsPageData(r, s.notice("Could not save the request-rate settings.", err)))
		return
	}
	s.render(w, "http-ratelimit-panel", s.limitsPageData(r, "Request-rate settings saved. Every HTTP daemon applies them within a minute, no restart."))
}

// fillSizeLimits sets each protocol's cap (in whole MB) on a page-data map, using the
// stored values or the built-in defaults. Shared by the Limits page and the unified
// Settings page so both render the same limits-panel.
func (s *Server) fillSizeLimits(data map[string]any) {
	imapMB, ewsMB, easMB := int64(defaultIMAPLiteralMB), int64(defaultEWSRequestMB), int64(defaultActiveSyncRequestMB)
	icalMB, vcardMB := int64(defaultDAVICalMB), int64(defaultDAVVCardMB)
	webMB, mapiMB := int64(defaultWebmailRequestMB), int64(defaultMapiRequestMB)
	if sl, found, err := s.dir.GetSizeLimits(); err == nil && found {
		imapMB = sl.IMAPLiteralBytes / (1024 * 1024)
		ewsMB = sl.EWSRequestBytes / (1024 * 1024)
		easMB = sl.ActiveSyncRequestBytes / (1024 * 1024)
		icalMB = sl.DAVICalBytes / (1024 * 1024)
		vcardMB = sl.DAVVCardBytes / (1024 * 1024)
		webMB = sl.WebmailRequestBytes / (1024 * 1024)
		mapiMB = sl.MapiRequestBytes / (1024 * 1024)
	}
	data["IMAPLiteralMB"] = imapMB
	data["EWSRequestMB"] = ewsMB
	data["ActiveSyncRequestMB"] = easMB
	data["DAVICalMB"] = icalMB
	data["DAVVCardMB"] = vcardMB
	data["WebmailRequestMB"] = webMB
	data["MapiRequestMB"] = mapiMB
}

// handleUISaveLimits persists the protocol size limits (entered in whole MB). Each
// protocol daemon applies its own value within about a minute, no restart. A value
// below 1 is rejected.
func (s *Server) handleUISaveLimits(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	imapMB, ewsMB, easMB := formInt(r, "imap_literal_mb"), formInt(r, "ews_request_mb"), formInt(r, "activesync_request_mb")
	icalMB, vcardMB := formInt(r, "dav_ical_mb"), formInt(r, "dav_vcard_mb")
	webMB, mapiMB := formInt(r, "webmail_request_mb"), formInt(r, "mapi_request_mb")
	if imapMB < 1 || ewsMB < 1 || easMB < 1 || icalMB < 1 || vcardMB < 1 || webMB < 1 || mapiMB < 1 {
		s.render(w, "limits-panel", s.limitsPageData(r, "Each limit must be at least 1 MB; settings not saved."))
		return
	}
	limits := directory.SizeLimits{
		IMAPLiteralBytes:       int64(imapMB) * 1024 * 1024,
		EWSRequestBytes:        int64(ewsMB) * 1024 * 1024,
		ActiveSyncRequestBytes: int64(easMB) * 1024 * 1024,
		DAVICalBytes:           int64(icalMB) * 1024 * 1024,
		DAVVCardBytes:          int64(vcardMB) * 1024 * 1024,
		WebmailRequestBytes:    int64(webMB) * 1024 * 1024,
		MapiRequestBytes:       int64(mapiMB) * 1024 * 1024,
	}
	if err := s.dir.SetSizeLimits(limits); err != nil {
		s.render(w, "limits-panel", s.limitsPageData(r, s.notice("Could not save the size limits.", err)))
		return
	}
	s.render(w, "limits-panel", s.limitsPageData(r, "Size limits saved. Each protocol applies its own within a minute, no restart."))
}

// fillFetchPolicy sets the fetch worker's source policy on a page-data map. The
// default is the worker's own: internal source addresses refused.
func (s *Server) fillFetchPolicy(data map[string]any) {
	data["FetchAllowInternal"] = false
	if st, found, err := s.dir.GetFetchSettings(); err == nil && found {
		data["FetchAllowInternal"] = st.AllowInternalSources
	}
}

// handleUISaveFetchPolicy persists the fetch worker's source policy. It is a full
// system-administrator decision (uiAuthorized enforces that): a fetchmail entry is
// created by a domain-scoped admin, so letting that role also lift the address
// block would leave the block guarding nothing.
func (s *Server) handleUISaveFetchPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	st := directory.FetchSettings{AllowInternalSources: r.PostFormValue("fetch_allow_internal") != ""}
	if err := s.dir.SetFetchSettings(st); err != nil {
		s.render(w, "fetchpolicy-panel", s.limitsPageData(r, s.notice("Could not save the fetch policy.", err)))
		return
	}
	s.render(w, "fetchpolicy-panel", s.limitsPageData(r, "Fetch policy saved. The fetch worker applies it within a minute, no restart."))
}

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
	// defaultFreeBusyTargets is a count, not a size: how many mailboxes one
	// availability request may fan out to. It mirrors each daemon's built-in value.
	defaultFreeBusyTargets = 100
	// defaultWebmailPreviewMB mirrors webmail2api's built-in inline-preview cap: the
	// largest attachment the message view previews without being asked.
	defaultWebmailPreviewMB = 2
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
	s.fillConnLimit(data)
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

// defaultConnMaxTotal and defaultConnMaxPerClient mirror the limiter's own built-in
// values (1000 connections per daemon, 20 per client address), shown on the page
// until an operator saves one.
const (
	defaultConnMaxTotal     = 1000
	defaultConnMaxPerClient = 20
)

// fillConnLimit sets the concurrent-connection cap's toggle and tunables on a
// page-data map, using the stored values or the limiter's built-in defaults
// (disabled). Shared by the Limits page and the unified Settings page.
func (s *Server) fillConnLimit(data map[string]any) {
	data["ConnLimitEnabled"] = false
	data["ConnMaxTotal"], data["ConnMaxPerClient"] = defaultConnMaxTotal, defaultConnMaxPerClient
	if st, found, err := s.dir.GetConnLimitSettings(); err == nil && found {
		data["ConnLimitEnabled"] = st.Enabled
		data["ConnMaxTotal"] = st.MaxTotal
		data["ConnMaxPerClient"] = st.MaxPerClient
	}
}

// handleUISaveConnLimit persists the concurrent-connection cap. Every IMAP, POP3
// and SMTP daemon applies the change within about a minute, no restart. A value
// below 1 is rejected so the cap is never configured to admit no connection.
func (s *Server) handleUISaveConnLimit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	total, perClient := formInt(r, "conn_max_total"), formInt(r, "conn_max_per_client")
	if total < 1 || perClient < 1 {
		s.render(w, "conn-limit-panel", s.limitsPageData(r, "Both connection caps must be at least 1; settings not saved."))
		return
	}
	st := directory.ConnLimitSettings{
		Enabled:      r.FormValue("enabled") == "1",
		MaxTotal:     total,
		MaxPerClient: perClient,
	}
	if err := s.dir.SetConnLimitSettings(st); err != nil {
		s.render(w, "conn-limit-panel", s.limitsPageData(r, s.notice("Could not save the connection caps.", err)))
		return
	}
	s.render(w, "conn-limit-panel", s.limitsPageData(r, "Connection caps saved. Every IMAP, POP3 and SMTP daemon applies them within a minute, no restart."))
}

// fillSizeLimits sets each protocol's cap (in whole MB) on a page-data map, using the
// stored values or the built-in defaults. Shared by the Limits page and the unified
// Settings page so both render the same limits-panel.
func (s *Server) fillSizeLimits(data map[string]any) {
	imapMB, ewsMB, easMB := int64(defaultIMAPLiteralMB), int64(defaultEWSRequestMB), int64(defaultActiveSyncRequestMB)
	icalMB, vcardMB := int64(defaultDAVICalMB), int64(defaultDAVVCardMB)
	webMB, mapiMB := int64(defaultWebmailRequestMB), int64(defaultMapiRequestMB)
	fbTargets, previewMB := int64(defaultFreeBusyTargets), int64(defaultWebmailPreviewMB)
	if sl, found, err := s.dir.GetSizeLimits(); err == nil && found {
		imapMB = sl.IMAPLiteralBytes / (1024 * 1024)
		ewsMB = sl.EWSRequestBytes / (1024 * 1024)
		easMB = sl.ActiveSyncRequestBytes / (1024 * 1024)
		icalMB = sl.DAVICalBytes / (1024 * 1024)
		vcardMB = sl.DAVVCardBytes / (1024 * 1024)
		webMB = sl.WebmailRequestBytes / (1024 * 1024)
		mapiMB = sl.MapiRequestBytes / (1024 * 1024)
		fbTargets = sl.FreeBusyMaxTargets
		previewMB = sl.WebmailPreviewMaxBytes / (1024 * 1024)
	}
	s.fillCommandLineLimits(data)
	data["IMAPLiteralMB"] = imapMB
	data["EWSRequestMB"] = ewsMB
	data["ActiveSyncRequestMB"] = easMB
	data["DAVICalMB"] = icalMB
	data["DAVVCardMB"] = vcardMB
	data["WebmailRequestMB"] = webMB
	data["MapiRequestMB"] = mapiMB
	data["FreeBusyMaxTargets"] = fbTargets
	data["WebmailPreviewMB"] = previewMB
}

// defaultIMAPCommandLineBytes, defaultPOP3CommandLineBytes and
// defaultSMTPCommandLineBytes mirror each daemon's own built-in line cap, shown on
// the page until an operator saves one.
const (
	defaultIMAPCommandLineBytes = 65536
	defaultPOP3CommandLineBytes = 8192
	defaultSMTPCommandLineBytes = 512
)

// fillCommandLineLimits sets the per-protocol command-line caps on a page-data map.
// They are shown in BYTES, not megabytes: one line of a mail protocol is small, and
// the SMTP figure is the 512 octets its RFC gives a command line.
func (s *Server) fillCommandLineLimits(data map[string]any) {
	imap, pop3, smtp := int64(defaultIMAPCommandLineBytes), int64(defaultPOP3CommandLineBytes), int64(defaultSMTPCommandLineBytes)
	if sl, found, err := s.dir.GetSizeLimits(); err == nil && found {
		imap, pop3, smtp = sl.IMAPCommandLineBytes, sl.POP3CommandLineBytes, sl.SMTPCommandLineBytes
	}
	data["IMAPCommandLineBytes"] = imap
	data["POP3CommandLineBytes"] = pop3
	data["SMTPCommandLineBytes"] = smtp
}

// handleUISaveLimits persists the protocol size limits (entered in whole MB). Each
// protocol daemon applies its own value within about a minute, no restart. A value
// below 1 is rejected.
func (s *Server) handleUISaveLimits(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	mb, ok := readSizeLimitMB(r)
	if !ok {
		s.render(w, "limits-panel", s.limitsPageData(r, "Each limit must be at least 1 MB; settings not saved."))
		return
	}
	// A count of mailboxes, not a size, so it is validated on its own and never
	// scaled by the megabyte factor the fields above use.
	fbTargets := formInt(r, "freebusy_max_targets")
	if fbTargets < 1 {
		s.render(w, "limits-panel", s.limitsPageData(r, "The free/busy target cap must be at least 1; settings not saved."))
		return
	}
	// The command-line caps are in bytes, so they are read and validated on their
	// own too. A line shorter than one command is unusable, so the floor is 64.
	imapLine, pop3Line, smtpLine := formInt(r, "imap_line_bytes"), formInt(r, "pop3_line_bytes"), formInt(r, "smtp_line_bytes")
	if imapLine < minCommandLineBytes || pop3Line < minCommandLineBytes || smtpLine < minCommandLineBytes {
		s.render(w, "limits-panel", s.limitsPageData(r,
			"Each command-line cap must be at least 64 bytes; settings not saved."))
		return
	}
	limits := directory.SizeLimits{
		IMAPLiteralBytes:       megabytes(mb["imap_literal_mb"]),
		EWSRequestBytes:        megabytes(mb["ews_request_mb"]),
		ActiveSyncRequestBytes: megabytes(mb["activesync_request_mb"]),
		DAVICalBytes:           megabytes(mb["dav_ical_mb"]),
		DAVVCardBytes:          megabytes(mb["dav_vcard_mb"]),
		WebmailRequestBytes:    megabytes(mb["webmail_request_mb"]),
		MapiRequestBytes:       megabytes(mb["mapi_request_mb"]),
		FreeBusyMaxTargets:     int64(fbTargets),
		WebmailPreviewMaxBytes: megabytes(mb["webmail_preview_mb"]),
		IMAPCommandLineBytes:   int64(imapLine),
		POP3CommandLineBytes:   int64(pop3Line),
		SMTPCommandLineBytes:   int64(smtpLine),
	}
	if err := s.dir.SetSizeLimits(limits); err != nil {
		s.render(w, "limits-panel", s.limitsPageData(r, s.notice("Could not save the size limits.", err)))
		return
	}
	s.render(w, "limits-panel", s.limitsPageData(r, "Size limits saved. Each protocol applies its own within a minute, no restart."))
}

// minCommandLineBytes is the smallest command-line cap an operator may save: below
// it no protocol command fits, so the daemon would refuse every client.
const minCommandLineBytes = 64

// sizeLimitFields are the megabyte-valued limits the form carries.
var sizeLimitFields = [...]string{
	"imap_literal_mb", "ews_request_mb", "activesync_request_mb", "dav_ical_mb",
	"dav_vcard_mb", "webmail_request_mb", "mapi_request_mb", "webmail_preview_mb",
}

// readSizeLimitMB reads every megabyte field, reporting false when any is below 1.
func readSizeLimitMB(r *http.Request) (map[string]int, bool) {
	mb := make(map[string]int, len(sizeLimitFields))
	for _, name := range sizeLimitFields {
		v := formInt(r, name)
		if v < 1 {
			return nil, false
		}
		mb[name] = v
	}
	return mb, true
}

// megabytes converts a limit entered in whole MB to the bytes it is stored as.
func megabytes(mb int) int64 { return int64(mb) * 1024 * 1024 }

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

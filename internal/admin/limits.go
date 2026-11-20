package admin

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"hermex/internal/activesync"
	"hermex/internal/authlimit"
	"hermex/internal/connlimit"
	"hermex/internal/dav"
	"hermex/internal/directory"
	"hermex/internal/ews"
	"hermex/internal/httplimit"
	"hermex/internal/imap"
	"hermex/internal/mapihttp"
	"hermex/internal/objectstore"
	"hermex/internal/pop3"
	"hermex/internal/smtp"
	"hermex/internal/webmail2api"
)

// The page shows each daemon's own built-in cap until an operator saves one, read
// from the package that enforces it so the two cannot drift apart.
const (
	defaultIMAPLiteralMB       = imap.DefaultMaxLiteralSize >> 20
	defaultEWSRequestMB        = ews.DefaultMaxRequestBody >> 20
	defaultActiveSyncRequestMB = activesync.DefaultMaxRequestBody >> 20
	defaultDAVICalMB           = dav.DefaultMaxICal >> 20
	defaultDAVVCardMB          = dav.DefaultMaxVCard >> 20
	defaultWebmailRequestMB    = webmail2api.DefaultMaxRequestBody >> 20
	defaultMapiRequestMB       = mapihttp.DefaultMaxRequestBody >> 20
	// defaultMapiUploadMB mirrors the object store's built-in cap on one message a
	// MAPI client uploads across many requests.
	defaultMapiUploadMB = objectstore.DefaultMaxUploadMessage >> 20
	// defaultFreeBusyTargets is a count, not a size: how many mailboxes one
	// availability request may fan out to. It mirrors each daemon's built-in value.
	defaultFreeBusyTargets = ews.DefaultFreeBusyTargets
	// defaultWebmailPreviewMB is webmail2api's built-in inline-preview cap: the
	// largest attachment the message view previews without being asked.
	defaultWebmailPreviewMB = webmail2api.DefaultMaxPreviewBytes >> 20
	// defaultEWSSubscriptionTimeoutMin is a duration in minutes, not a size: how
	// long an EWS notification subscription may stay unused before the server drops
	// it.
	defaultEWSSubscriptionTimeoutMin = ews.DefaultSubscriptionTimeoutMin
	// defaultTLSReportMB mirrors the MTA's built-in cap on an HTTPS TLS report body.
	defaultTLSReportMB = directory.DefaultTLSReportBytes >> 20
)

// minSubscriptionTimeoutMin and maxSubscriptionTimeoutMin bound the subscription
// idle timeout an operator may save. [MS-OXWSNTIF] 2.2.4.24 puts the ceiling at
// 1440 minutes (24 hours), and a timeout under a minute would drop a subscription
// between one client poll and the next.
const (
	minSubscriptionTimeoutMin = 1
	maxSubscriptionTimeoutMin = 1440
)

// handleUILimits renders the protocol size-limits page (system admins).
func (s *Server) handleUILimits(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, r, "limits.html", s.limitsPageData(r, panelNotice{}))
}

// limitsPageData builds the size-limits page model: each protocol cap shown in whole
// MB (the stored value, or the built-in default when none has been saved).
func (s *Server) limitsPageData(r *http.Request, notice panelNotice) map[string]any {
	failed := readFailures{}
	data := map[string]any{"Nav": "limits", "Notice": notice, "CSRF": csrfCookieValue(r), "ReadFailed": failed}
	s.addLimitSettings(data, failed)
	return data
}

// addLimitSettings merges the Limits cards' settings into a page's data. A setting
// that could not be read is recorded in failed, and its card shows that in place of
// its form, which would otherwise offer the defaults for a save to store. The Limits
// and Settings pages share it.
func (s *Server) addLimitSettings(data map[string]any, failed readFailures) {
	s.fillSizeLimits(data, failed)
	s.fillHTTPRateLimit(data, failed)
	s.fillConnLimit(data, failed)
	s.fillLoginLockout(data, failed)
	s.fillPasswordPolicy(data, failed)
	s.fillFetchPolicy(data, failed)
}

// fillPasswordPolicy sets the minimum password length on a page-data map: the
// stored value, or the directory's default while none has been saved.
func (s *Server) fillPasswordPolicy(data map[string]any, failed readFailures) {
	n, _, err := s.dir.GetMinPasswordLength()
	if !s.noteRead(failed, "passwordpolicy", "what.passwordPolicy", err) {
		return
	}
	data["MinPasswordLength"] = n
	data["MaxMinPasswordLength"] = directory.MaxMinPasswordLength
}

// handleUISavePasswordPolicy stores the minimum password length. The directory
// reads it on every password it stores, so the next account created, reset or
// changed is held to it, with no restart. Passwords already stored are not
// re-checked.
func (s *Server) handleUISavePasswordPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	n := formInt(r, "min_password_length")
	if n < 1 || n > directory.MaxMinPasswordLength {
		s.render(w, r, "passwordpolicy-panel", s.limitsPageData(r, errorNotice(msg("limits.passwordInvalid", strconv.Itoa(directory.MaxMinPasswordLength)))))
		return
	}
	if err := s.dir.SetMinPasswordLength(n); err != nil {
		s.render(w, r, "passwordpolicy-panel", s.limitsPageData(r, s.failNotice("limits.passwordFailed", err)))
		return
	}
	s.render(w, r, "passwordpolicy-panel", s.limitsPageData(r, okNotice("limits.passwordSaved")))
}

// shortPasswordNotice returns the panel message for a password the directory
// refused as shorter than the minimum, naming the minimum, and false for any
// other error.
func shortPasswordNotice(err error) (string, bool) {
	n, ok := directory.MinPasswordLength(err)
	if !ok {
		return "", false
	}
	return msg("changePassword.tooShort", strconv.Itoa(n)), true
}

// writeShortPassword answers a password the directory refused as too short with
// a 400 naming the minimum, and reports whether it did.
func writeShortPassword(w http.ResponseWriter, err error) bool {
	n, ok := directory.MinPasswordLength(err)
	if !ok {
		return false
	}
	http.Error(w, "the password must be at least "+strconv.Itoa(n)+" characters", http.StatusBadRequest)
	return true
}

// fillLoginLockout sets the failed-login limiter's tunables on a page-data map,
// using the stored values or the limiter's built-in defaults.
func (s *Server) fillLoginLockout(data map[string]any, failed readFailures) {
	st, found, err := s.dir.GetLoginLockoutSettings()
	if !s.noteRead(failed, "loginlockout", "what.loginLockout", err) {
		return
	}
	if !found {
		// The login limiter's own built-in tuning, the one in force until a save.
		st = directory.LoginLockoutSettings{
			MaxFails:       authlimit.DefaultMaxFails,
			WindowSeconds:  int(authlimit.DefaultWindow / time.Second),
			LockoutSeconds: int(authlimit.DefaultLockout / time.Second),
		}
	}
	data["LoginMaxFails"] = st.MaxFails
	data["LoginWindow"] = st.WindowSeconds
	data["LoginLockout"] = st.LockoutSeconds
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
		s.render(w, r, "loginlockout-panel", s.limitsPageData(r, errorNotice("limits.lockoutInvalid")))
		return
	}
	st := directory.LoginLockoutSettings{MaxFails: fails, WindowSeconds: window, LockoutSeconds: lockout}
	if err := s.dir.SetLoginLockoutSettings(st); err != nil {
		s.render(w, r, "loginlockout-panel", s.limitsPageData(r, s.failNotice("limits.lockoutFailed", err)))
		return
	}
	s.render(w, r, "loginlockout-panel", s.limitsPageData(r, okNotice("limits.lockoutSaved")))
}

// defaultHTTPRateBurst and defaultHTTPRateWindow are the limiter's own built-in
// values, shown on the page until an operator saves one.
const (
	defaultHTTPRateBurst  = httplimit.DefaultBurst
	defaultHTTPRateWindow = int(httplimit.DefaultWindow / time.Second)
)

// fillHTTPRateLimit sets the per-client HTTP request limiter's toggle and tunables on
// a page-data map, using the stored values or the limiter's built-in defaults
// (disabled). Shared by the Limits page and the unified Settings page.
func (s *Server) fillHTTPRateLimit(data map[string]any, failed readFailures) {
	st, found, err := s.dir.GetHTTPRateLimitSettings()
	if !s.noteRead(failed, "http-ratelimit", "what.requestRate", err) {
		return
	}
	if !found {
		st = directory.HTTPRateLimitSettings{Burst: defaultHTTPRateBurst, WindowSeconds: defaultHTTPRateWindow}
	}
	data["HTTPRateEnabled"] = st.Enabled
	data["HTTPRateBurst"] = st.Burst
	data["HTTPRateWindow"] = st.WindowSeconds
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
		s.render(w, r, "http-ratelimit-panel", s.limitsPageData(r, errorNotice("antispam.ratelimitInvalid")))
		return
	}
	st := directory.HTTPRateLimitSettings{
		Enabled:       r.FormValue("enabled") == "1",
		Burst:         burst,
		WindowSeconds: window,
	}
	if err := s.dir.SetHTTPRateLimitSettings(st); err != nil {
		s.render(w, r, "http-ratelimit-panel", s.limitsPageData(r, s.failNotice("limits.rateFailed", err)))
		return
	}
	s.render(w, r, "http-ratelimit-panel", s.limitsPageData(r, okNotice("limits.rateSaved")))
}

// fillConnLimit sets the concurrent-connection cap's toggle and tunables on a
// page-data map, using the stored values or the limiter's built-in defaults
// (disabled). Shared by the Limits page and the unified Settings page.
func (s *Server) fillConnLimit(data map[string]any, failed readFailures) {
	st, found, err := s.dir.GetConnLimitSettings()
	if !s.noteRead(failed, "conn-limit", "what.connLimit", err) {
		return
	}
	if !found {
		// The limiter's own built-in caps, the ones in force until a save.
		st = directory.ConnLimitSettings{MaxTotal: connlimit.DefaultMaxTotal, MaxPerClient: connlimit.DefaultMaxPerClient}
	}
	data["ConnLimitEnabled"] = st.Enabled
	data["ConnMaxTotal"] = st.MaxTotal
	data["ConnMaxPerClient"] = st.MaxPerClient
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
		s.render(w, r, "conn-limit-panel", s.limitsPageData(r, errorNotice("limits.connInvalid")))
		return
	}
	st := directory.ConnLimitSettings{
		Enabled:      r.FormValue("enabled") == "1",
		MaxTotal:     total,
		MaxPerClient: perClient,
	}
	if err := s.dir.SetConnLimitSettings(st); err != nil {
		s.render(w, r, "conn-limit-panel", s.limitsPageData(r, s.failNotice("limits.connFailed", err)))
		return
	}
	s.render(w, r, "conn-limit-panel", s.limitsPageData(r, okNotice("limits.connSaved")))
}

// fillSizeLimits sets each protocol's cap (in whole MB) on a page-data map, using the
// stored values or the built-in defaults. Shared by the Limits page and the unified
// Settings page so both render the same limits-panel. The one stored row feeds every
// field of the form, so a failed read of it is recorded in failed and fills none.
func (s *Server) fillSizeLimits(data map[string]any, failed readFailures) {
	sl, found, err := s.dir.GetSizeLimits()
	if !s.noteRead(failed, "limits", "what.sizeLimits", err) {
		return
	}
	imapMB, ewsMB, easMB := int64(defaultIMAPLiteralMB), int64(defaultEWSRequestMB), int64(defaultActiveSyncRequestMB)
	icalMB, vcardMB := int64(defaultDAVICalMB), int64(defaultDAVVCardMB)
	webMB, mapiMB := int64(defaultWebmailRequestMB), int64(defaultMapiRequestMB)
	fbTargets, previewMB := int64(defaultFreeBusyTargets), int64(defaultWebmailPreviewMB)
	tlsReportMB := int64(defaultTLSReportMB)
	if found {
		tlsReportMB = sl.TLSReportBytes / (1024 * 1024)
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
	fillCommandLineLimits(data, sl, found)
	fillMapiUpload(data, sl, found)
	data["IMAPLiteralMB"] = imapMB
	data["EWSRequestMB"] = ewsMB
	data["ActiveSyncRequestMB"] = easMB
	data["DAVICalMB"] = icalMB
	data["DAVVCardMB"] = vcardMB
	data["WebmailRequestMB"] = webMB
	data["MapiRequestMB"] = mapiMB
	data["FreeBusyMaxTargets"] = fbTargets
	data["WebmailPreviewMB"] = previewMB
	data["TLSReportMB"] = tlsReportMB
	fillSubscriptionTimeout(data, sl, found)
}

// fillMapiUpload sets the cap on one uploaded MAPI message on a page-data map. It
// is filled on its own because fillSizeLimits is already at its branch budget.
func fillMapiUpload(data map[string]any, sl directory.SizeLimits, found bool) {
	mb := int64(defaultMapiUploadMB)
	if found {
		mb = sl.MapiUploadMessageBytes / (1024 * 1024)
	}
	data["MapiUploadMB"] = mb
}

// fillSubscriptionTimeout sets the EWS notification-subscription idle timeout on a
// page-data map. It is a duration in minutes, so it is filled on its own rather
// than through the megabyte fields above.
func fillSubscriptionTimeout(data map[string]any, sl directory.SizeLimits, found bool) {
	mins := int64(defaultEWSSubscriptionTimeoutMin)
	if found && sl.EWSSubscriptionTimeoutMinutes > 0 {
		mins = sl.EWSSubscriptionTimeoutMinutes
	}
	data["EWSSubscriptionTimeoutMin"] = mins
}

// defaultIMAPCommandLineBytes, defaultPOP3CommandLineBytes and
// defaultSMTPCommandLineBytes are each daemon's own built-in line cap, shown on
// the page until an operator saves one.
const (
	defaultIMAPCommandLineBytes = imap.DefaultMaxCommandLine
	defaultPOP3CommandLineBytes = pop3.DefaultMaxCommandLine
	defaultSMTPCommandLineBytes = smtp.DefaultMaxCommandLine
)

// fillCommandLineLimits sets the per-protocol command-line caps on a page-data map.
// They are shown in BYTES, not megabytes: one line of a mail protocol is small, and
// the SMTP figure is the 512 octets its RFC gives a command line.
func fillCommandLineLimits(data map[string]any, sl directory.SizeLimits, found bool) {
	imap, pop3, smtp := int64(defaultIMAPCommandLineBytes), int64(defaultPOP3CommandLineBytes), int64(defaultSMTPCommandLineBytes)
	if found {
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
		s.render(w, r, "limits-panel", s.limitsPageData(r, errorNotice("limits.sizeInvalid")))
		return
	}
	// A count of mailboxes, not a size, so it is validated on its own and never
	// scaled by the megabyte factor the fields above use.
	fbTargets := formInt(r, "freebusy_max_targets")
	if fbTargets < 1 {
		s.render(w, r, "limits-panel", s.limitsPageData(r, errorNotice("limits.freebusyInvalid")))
		return
	}
	// The command-line caps are in bytes, so they are read and validated on their
	// own too. A line shorter than one command is unusable, so the floor is 64.
	imapLine, pop3Line, smtpLine := formInt(r, "imap_line_bytes"), formInt(r, "pop3_line_bytes"), formInt(r, "smtp_line_bytes")
	if imapLine < minCommandLineBytes || pop3Line < minCommandLineBytes || smtpLine < minCommandLineBytes {
		s.render(w, r, "limits-panel", s.limitsPageData(r, errorNotice("limits.lineInvalid")))
		return
	}
	// A duration in minutes, so it is read and validated on its own too. The
	// ceiling is the spec's; below the floor a subscription would be dropped
	// between one client poll and the next.
	subTimeout := formInt(r, "ews_subscription_timeout_min")
	if subTimeout < minSubscriptionTimeoutMin || subTimeout > maxSubscriptionTimeoutMin {
		s.render(w, r, "limits-panel", s.limitsPageData(r, errorNotice("limits.subsInvalid")))
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
		TLSReportBytes:         megabytes(mb["tlsrpt_mb"]),
		MapiUploadMessageBytes: megabytes(mb["mapi_upload_mb"]),
		IMAPCommandLineBytes:   int64(imapLine),
		POP3CommandLineBytes:   int64(pop3Line),
		SMTPCommandLineBytes:   int64(smtpLine),

		EWSSubscriptionTimeoutMinutes: int64(subTimeout),
	}
	if err := s.dir.SetSizeLimits(limits); err != nil {
		s.render(w, r, "limits-panel", s.limitsPageData(r, s.failNotice("limits.sizeFailed", err)))
		return
	}
	s.render(w, r, "limits-panel", s.limitsPageData(r, okNotice("limits.sizeSaved")))
}

// minCommandLineBytes is the smallest command-line cap an operator may save: below
// it no protocol command fits, so the daemon would refuse every client.
const minCommandLineBytes = 64

// sizeLimitFields are the megabyte-valued limits the form carries.
var sizeLimitFields = [...]string{
	"imap_literal_mb", "ews_request_mb", "activesync_request_mb", "dav_ical_mb",
	"dav_vcard_mb", "webmail_request_mb", "mapi_request_mb", "webmail_preview_mb", "tlsrpt_mb",
	"mapi_upload_mb",
}

// readSizeLimitMB reads every megabyte field, reporting false when any is below 1.
func readSizeLimitMB(r *http.Request) (map[string]int, bool) {
	mb := make(map[string]int, len(sizeLimitFields))
	for _, name := range sizeLimitFields {
		v := formInt(r, name)
		if v < 1 || !fitsInBytes(int64(v), 20) {
			return nil, false
		}
		mb[name] = v
	}
	return mb, true
}

// fitsInBytes reports whether n units of 2^shift bytes fit in an int64. A size
// entered past that wraps when scaled, to zero or a negative byte count.
func fitsInBytes(n int64, shift uint) bool {
	return n <= math.MaxInt64>>shift
}

// megabytes converts a limit entered in whole MB to the bytes it is stored as.
func megabytes(mb int) int64 { return int64(mb) * 1024 * 1024 }

// fillFetchPolicy sets the fetch worker's source policy on a page-data map. The
// default is the worker's own: internal source addresses refused.
func (s *Server) fillFetchPolicy(data map[string]any, failed readFailures) {
	st, found, err := s.dir.GetFetchSettings()
	if s.noteRead(failed, "fetchpolicy", "what.fetchPolicy", err) {
		data["FetchAllowInternal"] = found && st.AllowInternalSources
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
		s.render(w, r, "fetchpolicy-panel", s.limitsPageData(r, s.failNotice("limits.fetchFailed", err)))
		return
	}
	s.render(w, r, "fetchpolicy-panel", s.limitsPageData(r, okNotice("limits.fetchSaved")))
}

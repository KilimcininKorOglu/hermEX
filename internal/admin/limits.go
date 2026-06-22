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
	return data
}

// fillSizeLimits sets each protocol's cap (in whole MB) on a page-data map, using the
// stored values or the built-in defaults. Shared by the Limits page and the unified
// Settings page so both render the same limits-panel.
func (s *Server) fillSizeLimits(data map[string]any) {
	imapMB, ewsMB, easMB := int64(defaultIMAPLiteralMB), int64(defaultEWSRequestMB), int64(defaultActiveSyncRequestMB)
	icalMB, vcardMB := int64(defaultDAVICalMB), int64(defaultDAVVCardMB)
	if sl, found, err := s.dir.GetSizeLimits(); err == nil && found {
		imapMB = sl.IMAPLiteralBytes / (1024 * 1024)
		ewsMB = sl.EWSRequestBytes / (1024 * 1024)
		easMB = sl.ActiveSyncRequestBytes / (1024 * 1024)
		icalMB = sl.DAVICalBytes / (1024 * 1024)
		vcardMB = sl.DAVVCardBytes / (1024 * 1024)
	}
	data["IMAPLiteralMB"] = imapMB
	data["EWSRequestMB"] = ewsMB
	data["ActiveSyncRequestMB"] = easMB
	data["DAVICalMB"] = icalMB
	data["DAVVCardMB"] = vcardMB
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
	if imapMB < 1 || ewsMB < 1 || easMB < 1 || icalMB < 1 || vcardMB < 1 {
		s.render(w, "limits-panel", s.limitsPageData(r, "Each limit must be at least 1 MB; settings not saved."))
		return
	}
	limits := directory.SizeLimits{
		IMAPLiteralBytes:       int64(imapMB) * 1024 * 1024,
		EWSRequestBytes:        int64(ewsMB) * 1024 * 1024,
		ActiveSyncRequestBytes: int64(easMB) * 1024 * 1024,
		DAVICalBytes:           int64(icalMB) * 1024 * 1024,
		DAVVCardBytes:          int64(vcardMB) * 1024 * 1024,
	}
	if err := s.dir.SetSizeLimits(limits); err != nil {
		s.render(w, "limits-panel", s.limitsPageData(r, "Could not save the size limits: "+err.Error()))
		return
	}
	s.render(w, "limits-panel", s.limitsPageData(r, "Size limits saved — each protocol applies its own within a minute, no restart."))
}

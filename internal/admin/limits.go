package admin

import (
	"net/http"

	"hermex/internal/directory"
)

// defaultIMAPLiteralMB mirrors the IMAP server's built-in literal cap (50 MiB), shown
// on the page until an operator saves one. The server's own constant is unexported.
const defaultIMAPLiteralMB = 50

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
	imapMB := int64(defaultIMAPLiteralMB)
	if sl, found, err := s.dir.GetSizeLimits(); err == nil && found {
		imapMB = sl.IMAPLiteralBytes / (1024 * 1024)
	}
	return map[string]any{
		"Nav": "limits", "Notice": notice, "CSRF": csrfCookieValue(r),
		"IMAPLiteralMB": imapMB,
	}
}

// handleUISaveLimits persists the protocol size limits (entered in whole MB). Each
// protocol daemon applies its own value within about a minute, no restart. A value
// below 1 is rejected.
func (s *Server) handleUISaveLimits(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	imapMB := formInt(r, "imap_literal_mb")
	if imapMB < 1 {
		s.render(w, "limits-panel", s.limitsPageData(r, "The IMAP literal limit must be at least 1 MB; settings not saved."))
		return
	}
	if err := s.dir.SetSizeLimits(directory.SizeLimits{IMAPLiteralBytes: int64(imapMB) * 1024 * 1024}); err != nil {
		s.render(w, "limits-panel", s.limitsPageData(r, "Could not save the size limits: "+err.Error()))
		return
	}
	s.render(w, "limits-panel", s.limitsPageData(r, "Size limits saved — each protocol applies its own within a minute, no restart."))
}

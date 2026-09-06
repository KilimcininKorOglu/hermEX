package webmail2api

import (
	"net"
	"net/http"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/serve"
)

// sessionStore is the optional directory capability that records webmail2 logins so
// they can be listed and revoked. SQLDirectory implements it; when it is absent
// (e.g. static accounts) sessions stay stateless - no listing and no revocation.
type sessionStore interface {
	CreateWebmailSession(directory.WebmailSession) error
	WebmailSessionActive(jti string, now int64) (bool, error)
	TouchWebmailSession(jti string, now int64) error
	ListWebmailSessions(email string, now int64) ([]directory.WebmailSession, error)
	DeleteWebmailSession(email, jti string) (bool, error)
	DeleteOtherWebmailSessions(email, keepJti string) (int64, error)
}

// sessionJSON is the SPA's ClientSession shape.
type sessionJSON struct {
	ID         string `json:"id"`
	DeviceType string `json:"device_type"`
	ClientIP   string `json:"client_ip"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  string `json:"created_at"`
	LastActive string `json:"last_active"`
}

// recordLoginSession stores a freshly minted session. It is best-effort: when the
// store is absent it is a no-op, and the caller logs (never fails the login on) a
// store error - the token still authenticates statelessly.
func (s *Server) recordLoginSession(r *http.Request, email, jti string, created, expires time.Time) error {
	store, ok := s.auth.(sessionStore)
	if !ok {
		return nil
	}
	ua := r.Header.Get("User-Agent")
	return store.CreateWebmailSession(directory.WebmailSession{
		Jti:        jti,
		Email:      email,
		DeviceType: deviceLabel(ua),
		UserAgent:  ua,
		ClientIP:   clientIPOnly(serve.ClientAddr(r)),
		CreatedAt:  created.Unix(),
		LastActive: created.Unix(),
		ExpiresAt:  expires.Unix(),
	})
}

// sessionActive reports whether a verified token's server-side session is still
// valid. A token with no jti (minted before sessions existed) or an absent store
// passes (stateless fallback). A genuine revocation - the row deleted - fails it; a
// transient store error passes, so a DB blip never logs out an otherwise-valid
// token. It refreshes last-active when valid.
func (s *Server) sessionActive(c sessionClaims) bool {
	if c.Jti == "" {
		return true
	}
	store, ok := s.auth.(sessionStore)
	if !ok {
		return true
	}
	now := time.Now().Unix()
	active, err := store.WebmailSessionActive(c.Jti, now)
	if err != nil {
		return true
	}
	if active {
		_ = store.TouchWebmailSession(c.Jti, now)
	}
	return active
}

// revokeCurrentSession deletes the caller's own session row (on logout).
func (s *Server) revokeCurrentSession(c sessionClaims) {
	if c.Jti == "" {
		return
	}
	if store, ok := s.auth.(sessionStore); ok {
		if _, err := store.DeleteWebmailSession(c.Email, c.Jti); err != nil {
			logError("revoke-session", err, logging.Fields{"user": c.Email})
		}
	}
}

// handleSessions lists the caller's active sessions for the security UI.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	store, ok := s.auth.(sessionStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"sessions": []sessionJSON{}})
		return
	}
	rows, err := store.ListWebmailSessions(c.Email, time.Now().Unix())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	out := make([]sessionJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionJSON{
			ID:         row.Jti,
			DeviceType: row.DeviceType,
			ClientIP:   row.ClientIP,
			UserAgent:  row.UserAgent,
			CreatedAt:  time.Unix(row.CreatedAt, 0).UTC().Format(time.RFC3339),
			LastActive: time.Unix(row.LastActive, 0).UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleSessionRevoke revokes one of the caller's sessions by id (jti). It is scoped
// to the caller's email in the store, so a forged id cannot revoke another user's
// session.
func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	store, ok := s.auth.(sessionStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if _, err := store.DeleteWebmailSession(c.Email, r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "revoke failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// clientIPOnly strips a trailing :port from an address so the session list shows a
// bare IP; serve.ClientAddr yields a bare IP for an X-Forwarded-For hop but host:port
// for a direct RemoteAddr.
func clientIPOnly(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// deviceLabel summarizes a User-Agent into a "Browser on OS" label for the session
// list; it is a coarse best-effort, never parsed for anything load-bearing.
func deviceLabel(ua string) string {
	l := strings.ToLower(ua)
	browser := browserName(l)
	if os := osName(l); os != "" {
		return browser + " on " + os
	}
	return browser
}

// browserName picks the browser out of a lowercased User-Agent. The order
// matters: every one of these agents also claims the ones below it.
func browserName(l string) string {
	switch {
	case strings.Contains(l, "edg/"):
		return "Edge"
	case strings.Contains(l, "chrome/") && !strings.Contains(l, "chromium"):
		return "Chrome"
	case strings.Contains(l, "firefox/"):
		return "Firefox"
	case strings.Contains(l, "safari/") && !strings.Contains(l, "chrome"):
		return "Safari"
	}
	return "Browser"
}

// osName picks the operating system out of a lowercased User-Agent, reporting ""
// when none is recognisable.
func osName(l string) string {
	switch {
	case strings.Contains(l, "iphone"), strings.Contains(l, "ipad"):
		return "iOS"
	case strings.Contains(l, "android"):
		return "Android"
	case strings.Contains(l, "mac os"), strings.Contains(l, "macintosh"):
		return "macOS"
	case strings.Contains(l, "windows"):
		return "Windows"
	case strings.Contains(l, "linux"):
		return "Linux"
	}
	return ""
}

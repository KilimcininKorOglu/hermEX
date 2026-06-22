package admin

import (
	"net/http"
	"strings"
)

// senderRuleView is one allow/block rule rendered for the Sender allow/block page.
type senderRuleView struct {
	Pattern string
	Action  string
}

// senderAccessData builds the page model: the current allow/block rules.
func (s *Server) senderAccessData(r *http.Request, notice string) map[string]any {
	data := map[string]any{"Nav": "senderaccess", "CSRF": csrfCookieValue(r), "Notice": notice}
	rules, err := s.dir.ListSenderRules()
	if err != nil {
		data["Error"] = "Could not read the rules: " + err.Error()
	}
	views := make([]senderRuleView, 0, len(rules))
	for _, rule := range rules {
		views = append(views, senderRuleView{Pattern: rule.Pattern, Action: rule.Action})
	}
	data["Rules"] = views
	return data
}

// handleUISenderAccess renders the Sender allow/block page (system admins).
func (s *Server) handleUISenderAccess(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, "sender-access.html", s.senderAccessData(r, ""))
}

// handleUISaveSenderRule adds or flips an allow/block rule. The MTA hot-reloads it
// within about a minute, no restart.
func (s *Server) handleUISaveSenderRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	pattern := strings.TrimSpace(r.FormValue("pattern"))
	if pattern == "" {
		s.render(w, "sender-access-panel", s.senderAccessData(r, "A pattern (email address or domain) is required."))
		return
	}
	if err := s.dir.SetSenderRule(pattern, r.FormValue("action")); err != nil {
		s.render(w, "sender-access-panel", s.senderAccessData(r, "Could not save the rule: "+err.Error()))
		return
	}
	s.render(w, "sender-access-panel", s.senderAccessData(r, "Rule saved."))
}

// handleUIDeleteSenderRule removes an allow/block rule.
func (s *Server) handleUIDeleteSenderRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	if _, err := s.dir.DeleteSenderRule(strings.TrimSpace(r.FormValue("pattern"))); err != nil {
		s.render(w, "sender-access-panel", s.senderAccessData(r, "Could not delete the rule: "+err.Error()))
		return
	}
	s.render(w, "sender-access-panel", s.senderAccessData(r, "Rule removed."))
}

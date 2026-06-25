package webmail2api

import (
	"net/http"
	"strings"

	"hermex/internal/directory"
)

// recipientRuleStore is the directory capability for a user's personal allow/block
// rules. The MTA reads these at delivery (an allow rescues to the inbox, a block
// always files to Junk); webmail2 only manages them. SQLDirectory satisfies it.
type recipientRuleStore interface {
	ListRecipientRules(username string) ([]directory.RecipientRule, error)
	SetRecipientRule(username, pattern, action string) error
	DeleteRecipientRule(username, pattern string) (bool, error)
}

// recipientRuleJSON is the SPA shape for one personal allow/block rule.
type recipientRuleJSON struct {
	Pattern string `json:"pattern"`
	Action  string `json:"action"` // "allow" | "block"
}

func (s *Server) handleGetRecipientRules(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	store, ok := s.auth.(recipientRuleStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"rules": []recipientRuleJSON{}})
		return
	}
	rules, err := store.ListRecipientRules(c.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list rules"})
		return
	}
	out := make([]recipientRuleJSON, 0, len(rules))
	for _, ru := range rules {
		out = append(out, recipientRuleJSON{Pattern: ru.Pattern, Action: ru.Action})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

func (s *Server) handlePostRecipientRule(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req recipientRuleJSON
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if strings.TrimSpace(req.Pattern) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pattern is required"})
		return
	}
	if req.Action != directory.SenderAllow && req.Action != directory.SenderBlock {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be allow or block"})
		return
	}
	store, ok := s.auth.(recipientRuleStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "recipient rules are not available"})
		return
	}
	if err := store.SetRecipientRule(c.Email, req.Pattern, req.Action); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the rule"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteRecipientRule(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	pattern := r.URL.Query().Get("pattern")
	if pattern == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pattern is required"})
		return
	}
	store, ok := s.auth.(recipientRuleStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "recipient rules are not available"})
		return
	}
	if _, err := store.DeleteRecipientRule(c.Email, pattern); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete the rule"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

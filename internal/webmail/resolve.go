package webmail

import (
	"net/http"
	"net/mail"
	"strings"

	"hermex/internal/directory"
)

// galResultLimit caps how many GAL entries a suggestion or an ambiguous
// name-resolution returns, keeping the dropdown and the JSON payload bounded.
const galResultLimit = 10

// galSuggestion is one recipient suggestion: the address to insert and a display
// label for it (the address itself until per-user display names are stored — see
// directory.GALEntry).
type galSuggestion struct {
	Address string `json:"address"`
	Display string `json:"display"`
}

// resolveResult is one typed recipient's name-resolution outcome. Status is
// "resolved" (Matches holds the single chosen address), "ambiguous" (Matches
// holds the candidates to choose between), or "unresolved" (no match).
type resolveResult struct {
	Input   string          `json:"input"`
	Status  string          `json:"status"`
	Matches []galSuggestion `json:"matches,omitempty"`
}

// handleResolve backs recipient autocomplete and "check names" against the GAL.
// It is session-gated like every mailbox handler — an open address-book endpoint
// would leak the directory. With ?q it suggests addresses for a typed query;
// with ?check it resolves each comma-separated recipient to a known mailbox,
// flagging ambiguous and unresolved names. Both reply as JSON.
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessionFrom(r); !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	if query := strings.TrimSpace(q.Get("q")); query != "" {
		writeJSON(w, http.StatusOK, map[string]any{"suggestions": s.suggest(query, galResultLimit)})
		return
	}
	if check := strings.TrimSpace(q.Get("check")); check != "" {
		writeJSON(w, http.StatusOK, map[string]any{"results": s.checkNames(check)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": []galSuggestion{}})
}

// suggest returns GAL entries whose address matches query, as suggestions. It is
// empty when the directory cannot search the GAL or the search fails — webmail
// then simply offers no completions.
func (s *Server) suggest(query string, limit int) []galSuggestion {
	g, ok := s.accounts.(directory.GAL)
	if !ok {
		return []galSuggestion{}
	}
	entries, err := g.SearchGAL(query, limit)
	if err != nil {
		return []galSuggestion{}
	}
	out := make([]galSuggestion, 0, len(entries))
	for _, e := range entries {
		out = append(out, galSuggestion{Address: e.Address, Display: e.DisplayName})
	}
	return out
}

// checkNames resolves each comma-separated recipient in field, in order.
func (s *Server) checkNames(field string) []resolveResult {
	tokens := splitAddresses(field)
	out := make([]resolveResult, 0, len(tokens))
	for _, tok := range tokens {
		out = append(out, s.resolveToken(tok))
	}
	return out
}

// resolveToken resolves one typed recipient. A token the directory accepts
// directly (a known address, alias, or altname) is resolved as-is. Otherwise it
// is treated as a partial name and searched in the GAL: exactly one match
// resolves it, several are ambiguous, and none is unresolved.
func (s *Server) resolveToken(tok string) resolveResult {
	addr := bareAddress(tok)
	if _, ok := s.accounts.Resolve(addr); ok {
		return resolveResult{Input: tok, Status: "resolved", Matches: []galSuggestion{{Address: addr, Display: addr}}}
	}
	matches := s.suggest(addr, galResultLimit)
	switch len(matches) {
	case 0:
		return resolveResult{Input: tok, Status: "unresolved"}
	case 1:
		return resolveResult{Input: tok, Status: "resolved", Matches: matches}
	default:
		return resolveResult{Input: tok, Status: "ambiguous", Matches: matches}
	}
}

// bareAddress extracts the address from a token that may be a full "Name <addr>"
// form; a partial name (not a parseable address) is returned unchanged so it can
// be GAL-searched.
func bareAddress(tok string) string {
	if a, err := mail.ParseAddress(tok); err == nil {
		return a.Address
	}
	return tok
}

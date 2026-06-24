package webmail2api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// filterCondJSON / filterActionJSON / filterJSON mirror the SPA's Filter shapes.
type filterCondJSON struct {
	Field      string `json:"field"`
	Operator   string `json:"operator"`
	Value      string `json:"value"`
	HeaderName string `json:"headerName,omitempty"`
}

type filterActionJSON struct {
	Type      string `json:"type"`
	Target    string `json:"target,omitempty"`
	ForwardTo string `json:"forwardTo,omitempty"`
	Message   string `json:"message,omitempty"`
}

type filterJSON struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Enabled    bool               `json:"enabled"`
	MatchAll   bool               `json:"matchAll"`
	Conditions []filterCondJSON   `json:"conditions"`
	Actions    []filterActionJSON `json:"actions"`
	Priority   int                `json:"priority"`
}

func readFilters(m map[string]json.RawMessage) []filterJSON {
	var f []filterJSON
	if raw, ok := m["webmail2Filters"]; ok {
		_ = json.Unmarshal(raw, &f)
	}
	return f
}

func writeFilters(m map[string]json.RawMessage, f []filterJSON) {
	raw, _ := json.Marshal(f)
	m["webmail2Filters"] = raw
}

func (s *Server) handleGetFilters(w http.ResponseWriter, r *http.Request) {
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		return map[string]any{"filters": readFilters(m)}, false
	})
}

func (s *Server) handlePostFilter(w http.ResponseWriter, r *http.Request) {
	var in filterJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		filters := readFilters(m)
		in.ID = randomHex()[:8]
		in.Priority = len(filters)
		filters = append(filters, in)
		writeFilters(m, filters)
		rebuildInboxRules(st, filters)
		return in, true
	})
}

func (s *Server) handlePutFilter(w http.ResponseWriter, r *http.Request) {
	var in filterJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	id := r.PathValue("id")
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		filters := readFilters(m)
		for i := range filters {
			if filters[i].ID == id {
				in.ID = id
				in.Priority = filters[i].Priority
				filters[i] = in
				break
			}
		}
		writeFilters(m, filters)
		rebuildInboxRules(st, filters)
		return in, true
	})
}

func (s *Server) handleDeleteFilter(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		filters := readFilters(m)
		kept := filters[:0]
		for _, f := range filters {
			if f.ID != id {
				kept = append(kept, f)
			}
		}
		writeFilters(m, kept)
		rebuildInboxRules(st, kept)
		return map[string]bool{"ok": true}, true
	})
}

func (s *Server) handleReorderFilters(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FilterIDs []string `json:"filterIds"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		filters := readFilters(m)
		byID := make(map[string]filterJSON, len(filters))
		for _, f := range filters {
			byID[f.ID] = f
		}
		ordered := make([]filterJSON, 0, len(filters))
		for i, id := range body.FilterIDs {
			if f, ok := byID[id]; ok {
				f.Priority = i
				ordered = append(ordered, f)
				delete(byID, id)
			}
		}
		// Append any ids not mentioned (defensive), preserving them.
		for _, f := range filters {
			if _, ok := byID[f.ID]; ok {
				ordered = append(ordered, f)
			}
		}
		writeFilters(m, ordered)
		rebuildInboxRules(st, ordered)
		return map[string]bool{"ok": true}, true
	})
}

// handleRunFilters applies the Inbox's filter rules to the messages already in
// the Inbox on demand (the old webmail's "run now"), reporting how many messages
// were examined and how many a rule acted on. Incoming mail is filtered at
// delivery; this is the manual sweep over mail that arrived before the rule
// existed. It runs the stored rules as-is and never rebuilds them, so rules set
// by other clients are left untouched.
func (s *Server) handleRunFilters(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	res, err := st.RunRules(mapi.PrivateFIDInbox, time.Now().Unix())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not run filters"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"affected": res.Affected, "evaluated": res.Evaluated})
}

// rebuildInboxRules replaces the inbox's stored rules with the evaluatable subset
// of the SPA filters, so the supported conditions/actions actually fire at
// delivery. Filters with no supported condition or action are kept in the JSON
// (for the UI) but contribute no engine rule.
func rebuildInboxRules(st *objectstore.Store, filters []filterJSON) {
	if existing, err := st.ListRules(mapi.PrivateFIDInbox); err == nil {
		for _, r := range existing {
			_ = st.DeleteRule(r.ID)
		}
	}
	for i, f := range filters {
		if !f.Enabled {
			continue
		}
		cond, ok := filterCondition(f)
		if !ok {
			continue
		}
		blocks, stop, ok := filterActions(st, f)
		if !ok {
			continue
		}
		state := uint32(mapi.RuleStateEnabled)
		if stop {
			state |= mapi.RuleStateExitLevel
		}
		_, _ = st.AddRule(objectstore.Rule{
			FolderID:  mapi.PrivateFIDInbox,
			Name:      f.Name,
			Sequence:  int32(i),
			State:     state,
			Condition: cond,
			Actions:   mapi.RuleActions{Blocks: blocks},
		})
	}
}

// filterCondition maps a filter's supported conditions to a MAPI restriction.
func filterCondition(f filterJSON) (mapi.Restriction, bool) {
	var conds []mapi.Restriction
	for _, c := range f.Conditions {
		switch c.Field {
		case "subject":
			conds = append(conds, objectstore.RuleSubjectContains(c.Value))
		case "from", "address":
			conds = append(conds, objectstore.RuleFromContains(c.Value))
		case "body":
			conds = append(conds, objectstore.RuleBodyContains(c.Value))
		case "size":
			if n, err := strconv.Atoi(strings.TrimSpace(c.Value)); err == nil {
				conds = append(conds, objectstore.RuleSizeAtLeast(n))
			}
		}
	}
	switch len(conds) {
	case 0:
		return mapi.Restriction{}, false
	case 1:
		return conds[0], true
	default:
		if f.MatchAll {
			return objectstore.RuleAll(conds...), true
		}
		return objectstore.RuleAny(conds...), true
	}
}

// filterActions maps a filter's supported actions to MAPI action blocks, plus a
// stop-processing flag.
func filterActions(st *objectstore.Store, f filterJSON) (blocks []mapi.ActionBlock, stop bool, ok bool) {
	for _, a := range f.Actions {
		switch a.Type {
		case "moveToFolder":
			if fid, found := resolveFilterFolder(st, a.Target); found {
				blocks = append(blocks, objectstore.RuleMoveAction(fid))
			}
		case "copyToFolder":
			if fid, found := resolveFilterFolder(st, a.Target); found {
				blocks = append(blocks, objectstore.RuleCopyAction(fid))
			}
		case "delete":
			blocks = append(blocks, objectstore.RuleDeleteAction())
		case "markRead":
			blocks = append(blocks, objectstore.RuleMarkReadAction())
		case "forward", "redirect":
			if a.ForwardTo != "" {
				blocks = append(blocks, objectstore.RuleForwardAction(a.ForwardTo))
			} else if a.Target != "" {
				blocks = append(blocks, objectstore.RuleForwardAction(a.Target))
			}
		case "stop":
			stop = true
		}
	}
	return blocks, stop, len(blocks) > 0 || stop
}

// resolveFilterFolder resolves a filter target (a folder slug or display name) to
// a folder id.
func resolveFilterFolder(st *objectstore.Store, target string) (int64, bool) {
	if fid, ok := folderFID(strings.ToLower(target)); ok {
		return fid, true
	}
	return folderByName(st, target)
}

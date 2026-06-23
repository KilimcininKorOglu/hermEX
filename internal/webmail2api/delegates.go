package webmail2api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// delegationJSON is the stored form of a delegate grant. The grantee list is
// mirrored to the store's delegate list (the real access gate); the rights and
// send flags live here so the SPA round-trips them.
type delegationJSON struct {
	ID              string   `json:"id"`
	Grantee         string   `json:"grantee"`
	Rights          []string `json:"rights"`
	CanSendAs       bool     `json:"canSendAs"`
	CanSendOnBehalf bool     `json:"canSendOnBehalf"`
	CreatedAt       string   `json:"createdAt"`
}

func readDelegations(m map[string]json.RawMessage) []delegationJSON {
	var d []delegationJSON
	if raw, ok := m["webmail2Delegations"]; ok {
		_ = json.Unmarshal(raw, &d)
	}
	return d
}

func writeDelegations(m map[string]json.RawMessage, d []delegationJSON) {
	raw, _ := json.Marshal(d)
	m["webmail2Delegations"] = raw
}

// delegationOut builds the SPA's Delegation object. owner and mailbox are the
// authenticated user (self-service delegation is always over the own mailbox).
func delegationOut(owner string, d delegationJSON) map[string]any {
	return map[string]any{
		"id":              d.ID,
		"owner":           owner,
		"grantee":         d.Grantee,
		"mailbox":         owner,
		"rights":          strings.Join(d.Rights, ","),
		"canSendAs":       d.CanSendAs,
		"canSendOnBehalf": d.CanSendOnBehalf,
		"createdAt":       d.CreatedAt,
	}
}

// mirrorDelegates writes the grantee list to the store's delegate list so a
// delegate actually passes the shared-mailbox open gate (callerMayOpenShared).
func mirrorDelegates(st *objectstore.Store, dels []delegationJSON) {
	grantees := make([]string, 0, len(dels))
	for _, d := range dels {
		if g := strings.TrimSpace(d.Grantee); g != "" {
			grantees = append(grantees, g)
		}
	}
	_ = st.SetDelegates(grantees)
}

func (s *Server) handleGetDelegations(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	s.withSettings(w, r, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		dels := readDelegations(m)
		out := make([]map[string]any, 0, len(dels))
		for _, d := range dels {
			out = append(out, delegationOut(c.Email, d))
		}
		return map[string]any{"delegations": out}, false
	})
}

func (s *Server) handlePostDelegation(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var in struct {
		Grantee         string   `json:"grantee"`
		Rights          []string `json:"rights"`
		CanSendAs       bool     `json:"canSendAs"`
		CanSendOnBehalf bool     `json:"canSendOnBehalf"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if strings.TrimSpace(in.Grantee) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a grantee address is required"})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		dels := readDelegations(m)
		d := delegationJSON{
			ID:              randomHex()[:8],
			Grantee:         in.Grantee,
			Rights:          in.Rights,
			CanSendAs:       in.CanSendAs,
			CanSendOnBehalf: in.CanSendOnBehalf,
			CreatedAt:       now,
		}
		dels = append(dels, d)
		writeDelegations(m, dels)
		mirrorDelegates(st, dels)
		return delegationOut(c.Email, d), true
	})
}

func (s *Server) handleDeleteDelegation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		dels := readDelegations(m)
		kept := dels[:0]
		for _, d := range dels {
			if d.ID != id {
				kept = append(kept, d)
			}
		}
		writeDelegations(m, kept)
		mirrorDelegates(st, kept)
		return map[string]bool{"ok": true}, true
	})
}

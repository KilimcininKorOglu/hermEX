package webmail2api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// delegationJSON is what webmail keeps about a grant beyond the store: the id it
// hands the SPA and when the grant was made. The grant itself is the store's: who
// is a delegate and who may send are its delegate, send-as and send-on-behalf
// lists, and the access is the permission each delegate folder holds, the same
// lists and permissions Outlook, EWS and the admin panel write. Rights is always
// read back from those permissions.
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

// storeGrants are the three store lists a delegation is made of. The delegate
// list is the shared-mailbox open gate (callerMayOpenShared) and grants no send of
// its own; the send-as list puts only this mailbox on the message; the
// send-on-behalf list puts this mailbox in From and the delegate in Sender.
type storeGrants struct {
	delegates, sendAs, onBehalf []string
}

func loadGrants(st *objectstore.Store) (storeGrants, error) {
	var g storeGrants
	var err error
	if g.delegates, err = st.GetDelegates(); err != nil {
		return g, err
	}
	if g.sendAs, err = st.GetSendAs(); err != nil {
		return g, err
	}
	g.onBehalf, err = st.GetSendOnBehalf()
	return g, err
}

func (g storeGrants) save(st *objectstore.Store) error {
	if err := st.SetDelegates(g.delegates); err != nil {
		return err
	}
	if err := st.SetSendAs(g.sendAs); err != nil {
		return err
	}
	return st.SetSendOnBehalf(g.onBehalf)
}

// grantees lists everyone the store lists name, once each, in list order.
func (g storeGrants) grantees() []string {
	var out []string
	for _, list := range [][]string{g.delegates, g.onBehalf, g.sendAs} {
		for _, a := range list {
			if indexFold(out, a) < 0 {
				out = append(out, a)
			}
		}
	}
	return out
}

// set puts grantee on the delegate list and on each send list its flags ask for,
// and takes it off a send list whose flag is off.
func (g *storeGrants) set(grantee string, sendAs, onBehalf bool) {
	g.delegates = withMember(g.delegates, grantee, true)
	g.sendAs = withMember(g.sendAs, grantee, sendAs)
	g.onBehalf = withMember(g.onBehalf, grantee, onBehalf)
}

// remove takes grantee off every list.
func (g *storeGrants) remove(grantee string) {
	g.set(grantee, false, false)
	g.delegates = withMember(g.delegates, grantee, false)
}

// withMember returns list with addr present or absent, matched without regard to
// case, keeping every other entry and its order.
func withMember(list []string, addr string, present bool) []string {
	i := indexFold(list, addr)
	switch {
	case present && i < 0:
		return append(list, addr)
	case !present && i >= 0:
		return slices.Delete(slices.Clone(list), i, i+1)
	}
	return list
}

func indexFold(list []string, addr string) int {
	return slices.IndexFunc(list, func(a string) bool { return strings.EqualFold(a, addr) })
}

// grantID is the id of a grant webmail has no record of: stable across reads, so
// the SPA can delete it by the id it listed.
func grantID(grantee string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(grantee)))
	return "g" + hex.EncodeToString(sum[:4])
}

// delegationRows joins the store lists with webmail's own records: every grantee
// the lists name is a row, its send flags read from the lists, and its id, access
// and date taken from webmail's record when there is one.
func delegationRows(g storeGrants, saved []delegationJSON) []delegationJSON {
	grantees := g.grantees()
	rows := make([]delegationJSON, 0, len(grantees))
	for _, a := range grantees {
		row := delegationJSON{ID: grantID(a)}
		if i := slices.IndexFunc(saved, func(d delegationJSON) bool { return strings.EqualFold(d.Grantee, a) }); i >= 0 {
			row = saved[i]
		}
		row.Grantee = a
		row.CanSendAs = indexFold(g.sendAs, a) >= 0
		row.CanSendOnBehalf = indexFold(g.onBehalf, a) >= 0
		rows = append(rows, row)
	}
	return rows
}

// folderRights is the rights mask the SPA's access choice sets on each delegate
// folder: "write" makes the delegate an Editor, "read" a Reviewer, and no choice
// removes the folder grant.
func folderRights(rights []string) uint32 {
	var mask uint32
	switch {
	case slices.Contains(rights, "write"):
		mask = mapi.RightsEditor
	case slices.Contains(rights, "read"):
		mask = mapi.RightsReviewer
	default:
		return 0
	}
	return mapi.NormalizeRights(mask, true)
}

// accessNames reads the access a grantee holds back from the delegate folders,
// whichever surface granted it: "read" when any folder lets them read every item,
// "write" when any lets them delete every item, the right the shared-mailbox write
// gate checks.
func accessNames(byFolder map[int64]uint32) []string {
	var union uint32
	for _, r := range byFolder {
		union |= r
	}
	var out []string
	if union&mapi.FrightsReadAny != 0 {
		out = append(out, "read")
	}
	if union&mapi.FrightsDeleteAny != 0 {
		out = append(out, "write")
	}
	return out
}

// withFolderAccess sets each row's access to what the delegate folders grant.
func withFolderAccess(st *objectstore.Store, rows []delegationJSON) error {
	for i := range rows {
		byFolder, err := st.DelegateFolderRights(rows[i].Grantee)
		if err != nil {
			return err
		}
		rows[i].Rights = accessNames(byFolder)
	}
	return nil
}

func grantsUnavailable(err error) settingsFailure {
	return settingsFailure{status: http.StatusInternalServerError, msg: "delegates unavailable", event: "delegates.read", err: err}
}

func grantsUnsaved(err error) settingsFailure {
	return settingsFailure{status: http.StatusInternalServerError, msg: "could not save delegates", event: "delegates.write", err: err}
}

func (s *Server) handleGetDelegations(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		g, err := loadGrants(st)
		if err != nil {
			return grantsUnavailable(err), false
		}
		rows := delegationRows(g, readDelegations(m))
		if err := withFolderAccess(st, rows); err != nil {
			return grantsUnavailable(err), false
		}
		out := make([]map[string]any, 0, len(rows))
		for _, d := range rows {
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
	grantee := strings.TrimSpace(in.Grantee)
	if grantee == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a grantee address is required"})
		return
	}
	d := delegationJSON{
		ID:              randomHex()[:8],
		Grantee:         grantee,
		Rights:          in.Rights,
		CanSendAs:       in.CanSendAs,
		CanSendOnBehalf: in.CanSendOnBehalf,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		g, err := loadGrants(st)
		if err != nil {
			return grantsUnavailable(err), false
		}
		g.set(grantee, d.CanSendAs, d.CanSendOnBehalf)
		if err := g.save(st); err != nil {
			return grantsUnsaved(err), false
		}
		if err := st.SetDelegateFolderRights(grantee, folderRights(d.Rights)); err != nil {
			return grantsUnsaved(err), false
		}
		saved := slices.DeleteFunc(readDelegations(m), func(o delegationJSON) bool { return strings.EqualFold(o.Grantee, grantee) })
		writeDelegations(m, append(saved, d))
		return delegationOut(c.Email, d), true
	})
}

func (s *Server) handleDeleteDelegation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.withSettings(w, r, func(st *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		g, err := loadGrants(st)
		if err != nil {
			return grantsUnavailable(err), false
		}
		saved := readDelegations(m)
		rows := delegationRows(g, saved)
		i := slices.IndexFunc(rows, func(d delegationJSON) bool { return d.ID == id })
		if i < 0 {
			return settingsFailure{status: http.StatusNotFound, msg: "no such delegation"}, false
		}
		grantee := rows[i].Grantee
		g.remove(grantee)
		if err := g.save(st); err != nil {
			return grantsUnsaved(err), false
		}
		if err := st.SetDelegateFolderRights(grantee, 0); err != nil {
			return grantsUnsaved(err), false
		}
		writeDelegations(m, slices.DeleteFunc(saved, func(d delegationJSON) bool { return strings.EqualFold(d.Grantee, grantee) }))
		return map[string]bool{"ok": true}, true
	})
}

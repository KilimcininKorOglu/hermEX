package webmail

import (
	"archive/zip"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// maxExport caps a bulk EML export so one request cannot stream an unbounded
// archive (mirrors the reference's max-files limit on multi-select export).
const maxExport = 200

// handleBulk applies one action to every selected message and redirects back to
// the folder. It is the multi-select counterpart to /action: the message list
// wraps its rows in a form whose checkboxes post the selected uids here. Bulk
// EML export is handled separately by /export (it streams a file, not a redirect).
func (s *Server) handleBulk(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		http.Error(w, "cannot read folders", http.StatusInternalServerError)
		return
	}
	folder := r.FormValue("folder")
	folderID, found := resolveFolder(folders, folder)
	if !found {
		http.NotFound(w, r)
		return
	}
	uids := parseUIDs(r.Form["uid"])
	op := r.FormValue("op")
	for _, uid := range uids {
		applyBulk(st, folders, folderID, uid, op, r)
	}
	http.Redirect(w, r, "/mail?folder="+url.QueryEscape(folder), http.StatusSeeOther)
}

// applyBulk performs a single bulk op on one message, best-effort: an error on
// one message does not abort the batch (the redirect reflects whatever applied).
func applyBulk(st *objectstore.Store, folders []objectstore.FolderInfo, folderID int64, uid uint32, op string, r *http.Request) {
	switch op {
	case "read", "unread":
		cur, err := st.MessageFlags(folderID, uid)
		if err != nil {
			return
		}
		if op == "read" {
			cur |= objectstore.FlagSeen
		} else {
			cur &^= objectstore.FlagSeen
		}
		st.SetMessageFlags(folderID, uid, cur)
	case "flag":
		if m, err := st.MessageByUID(folderID, uid); err == nil {
			st.SetFollowupFlag(m.ID, objectstore.FollowupFlag{Status: objectstore.FlagStatusFlagged, Color: objectstore.FlagColorRed, Request: "Follow up"})
		}
	case "unflag":
		if m, err := st.MessageByUID(folderID, uid); err == nil {
			st.ClearFollowupFlag(m.ID)
		}
	case "categorize":
		name := r.FormValue("cat")
		if m, err := st.MessageByUID(folderID, uid); err == nil && name != "" {
			cur, _ := st.GetCategories(m.ID)
			if !containsStr(cur, name) {
				st.SetCategories(m.ID, append(cur, name))
			}
		}
	case "junk":
		if folderID != int64(mapi.PrivateFIDJunk) {
			moveMessage(st, folderID, uid, int64(mapi.PrivateFIDJunk))
		}
	case "delete":
		trash := int64(mapi.PrivateFIDDeletedItems)
		if folderID == trash {
			st.DeleteMessage(folderID, uid)
		} else {
			moveMessage(st, folderID, uid, trash)
		}
	case "move":
		if dst, ok := parseDst(r, folders); ok && dst != folderID {
			moveMessage(st, folderID, uid, dst)
		}
	}
}

// handleExport streams the selected messages as a zip of .eml files (the bulk
// RFC822 export), capped at maxExport entries.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		http.Error(w, "cannot read folders", http.StatusInternalServerError)
		return
	}
	folderID, found := resolveFolder(folders, r.FormValue("folder"))
	if !found {
		http.NotFound(w, r)
		return
	}
	uids := parseUIDs(r.Form["uid"])
	if len(uids) == 0 {
		http.Error(w, "no messages selected", http.StatusBadRequest)
		return
	}
	if len(uids) > maxExport {
		uids = uids[:maxExport]
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="messages.zip"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, uid := range uids {
		raw, err := st.GetMessageRaw(folderID, uid)
		if err != nil {
			continue
		}
		f, err := zw.Create("message-" + strconv.FormatUint(uint64(uid), 10) + ".eml")
		if err != nil {
			return
		}
		f.Write(raw)
	}
}

// parseUIDs converts a list of decimal uid strings to uint32, skipping bad ones.
func parseUIDs(raw []string) []uint32 {
	out := make([]uint32, 0, len(raw))
	for _, s := range raw {
		if n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32); err == nil {
			out = append(out, uint32(n))
		}
	}
	return out
}

// containsStr reports whether s is in xs.
func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

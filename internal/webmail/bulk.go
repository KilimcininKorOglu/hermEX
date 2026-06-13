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

// handleEML streams one message as a single .eml download (RFC822), named after
// its subject. The bulk /export gives a zip; this is the single-message variant.
func (s *Server) handleEML(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	folder := r.URL.Query().Get("folder")
	uid64, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	if err != nil {
		http.Error(w, "bad uid", http.StatusBadRequest)
		return
	}
	uid := uint32(uid64)
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
	folderID, found := resolveFolder(folders, folder)
	if !found {
		http.NotFound(w, r)
		return
	}
	raw, err := st.GetMessageRaw(folderID, uid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := "message.eml"
	if m, err := st.MessageByUID(folderID, uid); err == nil {
		name = safeEMLName(m.Subject)
	}
	w.Header().Set("Content-Type", "message/rfc822")
	// RFC 6266: an ASCII filename for legacy clients plus a UTF-8 filename* for
	// non-ASCII subjects (Turkish "Toplantı notları.eml" must survive intact).
	w.Header().Set("Content-Disposition", `attachment; filename="`+asciiFallback(name)+`"; filename*=UTF-8''`+rfc5987Encode(name))
	w.Write(raw)
}

// safeEMLName turns a subject into a .eml filename, replacing only path- and
// header-unsafe characters while preserving Unicode (e.g. Turkish letters). It
// falls back to "message.eml" for an empty or unusable subject.
func safeEMLName(subject string) string {
	cleaned := strings.Map(func(rc rune) rune {
		switch {
		case rc < 0x20, rc == '/', rc == '\\', rc == ':', rc == '*', rc == '?', rc == '"', rc == '<', rc == '>', rc == '|':
			return '_'
		}
		return rc
	}, subject)
	cleaned = strings.TrimSpace(cleaned)
	// Bound the length by runes, never bytes, so a multibyte rune is never split.
	if rs := []rune(cleaned); len(rs) > 120 {
		cleaned = strings.TrimSpace(string(rs[:120]))
	}
	if cleaned == "" {
		return "message.eml"
	}
	return cleaned + ".eml"
}

// asciiFallback replaces every non-ASCII rune with '_' for the legacy quoted
// filename= form, which must be ASCII (HTTP header values are not UTF-8).
func asciiFallback(s string) string {
	return strings.Map(func(rc rune) rune {
		if rc > 0x7f {
			return '_'
		}
		return rc
	}, s)
}

// rfc5987Encode percent-encodes a UTF-8 string for the filename* parameter,
// leaving only the RFC 3986 unreserved set unescaped (a superset-safe subset of
// RFC 5987's attr-char, so the result is always a valid filename* value).
func rfc5987Encode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
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

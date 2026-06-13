package webmail

import (
	"net/http"
	"sort"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// searchView is the data the search page renders.
type searchView struct {
	User      string
	Query     string
	Field     string // "all" (subject+sender+body+to+cc) | "subject"
	Scope     string // "folder" (this folder) | "all" (all mail folders)
	Current   string // the folder a "this folder" search is scoped to
	Folders   []folderView
	Results   []messageView
	Searched  bool     // false on first visit / empty query → prompt, not "no matches"
	Truncated []string // folder paths whose scan errored (results may be incomplete)
}

// handleSearch runs a server-side mail search: in the current folder or across
// all mail folders, over the subject (plus sender/to/cc/body for the "all" field
// scope). It mirrors handleMail's session+store flow and renders the dedicated
// search view. An empty query does no scan (the "all" scope would otherwise read
// every message's wire form).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	field := whitelist(r.URL.Query().Get("field"), "all", "subject")
	scope := whitelist(r.URL.Query().Get("scope"), "folder", "all")
	current := r.URL.Query().Get("scopefolder")
	if current == "" {
		current = inboxName
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

	views := buildFolderViews(folders)
	v := searchView{
		User:    sess.user,
		Query:   q,
		Field:   field,
		Scope:   scope,
		Current: current,
		Folders: views,
	}
	if q == "" { // first visit or cleared query: prompt, no scan
		s.render(w, "search", v)
		return
	}
	v.Searched = true

	// Resolve the target folders: the single current folder, or every mail folder.
	type target struct {
		id   int64
		path string
	}
	var targets []target
	if scope == "all" {
		mail := mailFolderIDs(st, folders)
		for i, f := range folders {
			if mail[f.ID] {
				targets = append(targets, target{id: f.ID, path: views[i].Path})
			}
		}
	} else if id, found := resolveFolder(folders, current); found {
		targets = append(targets, target{id: id, path: current})
	}

	terms := strings.Fields(strings.ToLower(q))
	type hit struct {
		info objectstore.MessageInfo
		id   int64
		path string
	}
	var hits []hit
	for _, t := range targets {
		msgs, err := st.ListMessages(t.id)
		if err != nil {
			v.Truncated = append(v.Truncated, t.path)
			continue
		}
		errored := false
		for _, m := range msgs {
			matched, fetchErrored := matchMessage(terms, m.Subject, m.Sender, field, func() (string, bool) {
				return expensiveText(st, t.id, m.UID)
			})
			if matched {
				hits = append(hits, hit{info: m, id: t.id, path: t.path})
			} else if fetchErrored {
				errored = true
			}
		}
		if errored {
			v.Truncated = append(v.Truncated, t.path)
		}
	}

	// Newest first across all folders.
	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].info.InternalDate.After(hits[j].info.InternalDate)
	})
	for _, h := range hits {
		v.Results = append(v.Results, messageViewFrom(h.id, h.path, h.info))
	}
	s.render(w, "search", v)
}

// matchMessage reports whether a message matches every search term (AND). The
// cheap index fields (subject, plus sender for the "all" scope) are tested first;
// only terms still unmatched trigger fetchExpensive (the to/cc/body text), and
// only for the "all" field scope. The split is per-term, so a query can match by
// combining a cheap field and an expensive one. fetchErrored is true when the
// expensive fetch failed for a message that had not already matched, letting the
// caller flag the folder as not fully searched.
func matchMessage(terms []string, subject, sender, field string, fetchExpensive func() (string, bool)) (matched, fetchErrored bool) {
	if len(terms) == 0 {
		return false, false
	}
	cheap := strings.ToLower(subject)
	if field == "all" {
		cheap += " " + strings.ToLower(sender)
	}
	var unmatched []string
	for _, t := range terms {
		if !strings.Contains(cheap, t) {
			unmatched = append(unmatched, t)
		}
	}
	if len(unmatched) == 0 {
		return true, false
	}
	if field != "all" {
		return false, false // Subject scope never reads the body
	}
	exp, ok := fetchExpensive()
	if !ok {
		return false, true
	}
	for _, t := range unmatched {
		if !strings.Contains(exp, t) {
			return false, false
		}
	}
	return true, false
}

// expensiveText returns the lowercased to+cc+body text of a message for the "all"
// field scope, reading and parsing the stored wire form. ok is false when the
// message could not be read.
func expensiveText(st *objectstore.Store, folderID int64, uid uint32) (string, bool) {
	raw, err := st.GetMessageRaw(folderID, uid)
	if err != nil {
		return "", false
	}
	var b strings.Builder
	if env, err := mime.ParseEnvelope(raw); err == nil && env != nil {
		b.WriteString(formatAddrs(env.To))
		b.WriteByte(' ')
		b.WriteString(formatAddrs(env.Cc))
		b.WriteByte(' ')
	}
	b.WriteString(bestTextBody(raw))
	return strings.ToLower(b.String()), true
}

// mailFolderIDs returns the set of mail-folder ids (container class IPF.Note) for
// a cross-folder search. PIM folders (calendar/contacts/tasks/notes/journal) are
// excluded; a folder whose class cannot be read is skipped. Deleted Items and
// Junk are mail folders, so a cross-folder search does find deleted and spam mail.
func mailFolderIDs(st *objectstore.Store, folders []objectstore.FolderInfo) map[int64]bool {
	out := make(map[int64]bool, len(folders))
	for _, f := range folders {
		props, err := st.GetFolderProperties(f.ID, mapi.PrContainerClass)
		if err != nil {
			continue
		}
		if cls, ok := props.Get(mapi.PrContainerClass); ok {
			if s, _ := cls.(string); s == mapi.ContainerClassNote {
				out[f.ID] = true
			}
		}
	}
	return out
}

// whitelist returns v if it is one of allowed, else the first allowed value (the
// default), guarding a query parameter against arbitrary input.
func whitelist(v string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return allowed[0]
}

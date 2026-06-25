package webmail2api

import (
	"archive/zip"
	"encoding/base64"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// maxImportBytes caps an imported .eml request body (base64 inflates ~33%, so
// this allows roughly a 30 MiB message).
const maxImportBytes = 40 << 20

// handleAttachment streams the Nth attachment of a message (the same walk order
// collectAttachments assigns).
func (s *Server) handleAttachment(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	index, _ := strconv.Atoi(r.URL.Query().Get("index"))
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	root := mime.ParseStructure(raw)
	var found *mime.Part
	idx := 0
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil || found != nil {
			return
		}
		name := p.DispParams["filename"]
		if name == "" {
			name = p.Params["name"]
		}
		if p.Type != "multipart" && (p.Disposition == "attachment" || name != "") {
			if idx == index {
				found = p
				return
			}
			idx++
		}
		for _, ch := range p.Children {
			walk(ch)
		}
	}
	walk(root)
	if found == nil {
		http.Error(w, "attachment not found", http.StatusNotFound)
		return
	}
	body, err := found.DecodedContent()
	if err != nil {
		http.Error(w, "cannot decode", http.StatusInternalServerError)
		return
	}
	filename := found.Filename()
	if filename == "" {
		filename = "attachment"
	}
	w.Header().Set("Content-Type", found.Type+"/"+found.Subtype)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	_, _ = w.Write(body)
}

// handleExport serves a message as a downloadable .eml file.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", "attachment; filename=\"message.eml\"")
	_, _ = w.Write(raw)
}

// maxBulkExport caps a bulk EML export so one request cannot stream an unbounded zip.
const maxBulkExport = 200

// handleExportBulk streams the selected messages as a zip of .eml files (the bulk
// RFC822 export). Messages are addressed by the same "<folder>:<uid>" ids the list
// view hands out and may span folders; each is gated by the folder's read
// permission and the count is capped at maxBulkExport. A streaming zip commits a
// 200 on the first byte, so every hard failure is reported before the zip starts
// and a per-message problem is skipped rather than surfaced.
func (s *Server) handleExportBulk(w http.ResponseWriter, r *http.Request) {
	mb, ok := s.openMailbox(w, r)
	if !ok {
		return
	}
	defer mb.st.Close()
	ids := r.URL.Query()["id"]
	if len(ids) == 0 {
		http.Error(w, "no messages selected", http.StatusBadRequest)
		return
	}
	if len(ids) > maxBulkExport {
		ids = ids[:maxBulkExport]
	}
	// Resolve each folder slug (and its read verdict) once: a 200-message export
	// must not run 200 ListFolders queries.
	type folderGate struct {
		fid int64
		ok  bool
	}
	gates := map[string]folderGate{}
	resolve := func(folder string) (int64, bool) {
		if g, seen := gates[folder]; seen {
			return g.fid, g.ok
		}
		fid, ok := resolveFolder(mb.st, folder)
		if ok {
			ok = mb.readAllowed(fid)
		}
		gates[folder] = folderGate{fid, ok}
		return fid, ok
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"messages.zip\"")
	zw := zip.NewWriter(w)
	defer zw.Close()
	used := map[string]bool{}
	for _, id := range ids {
		folder, uid, ok := parseMessageID(id)
		if !ok {
			continue
		}
		fid, ok := resolve(folder)
		if !ok {
			continue
		}
		raw, err := mb.st.GetMessageRaw(fid, uid)
		if err != nil {
			continue
		}
		// The same uid in two different folders would collide; disambiguate the
		// zip entry name so neither message is silently overwritten.
		name := "message-" + strconv.FormatUint(uint64(uid), 10) + ".eml"
		for n := 2; used[name]; n++ {
			name = "message-" + strconv.FormatUint(uint64(uid), 10) + "-" + strconv.Itoa(n) + ".eml"
		}
		used[name] = true
		f, err := zw.Create(name)
		if err != nil {
			return
		}
		_, _ = f.Write(raw)
	}
}

// handleSource serves a message's raw RFC822 source as inline text/plain, for the
// "view source / show original" action (own mailbox only, like the other locate-
// based readers).
func (s *Server) handleSource(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(raw)
}

// handleAttachmentsZip streams every attachment of a message as a single .zip
// (the same walk order handleAttachment indexes). Own mailbox only.
func (s *Server) handleAttachmentsZip(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"attachments.zip\"")
	zw := zip.NewWriter(w)
	defer zw.Close()
	idx := 0
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil {
			return
		}
		name := p.DispParams["filename"]
		if name == "" {
			name = p.Params["name"]
		}
		if p.Type != "multipart" && (p.Disposition == "attachment" || name != "") {
			if body, err := p.DecodedContent(); err == nil {
				fn := p.Filename()
				if fn == "" {
					fn = "attachment-" + strconv.Itoa(idx)
				}
				idx++
				if fw, err := zw.Create(fn); err == nil {
					_, _ = fw.Write(body)
				}
			}
		}
		for _, ch := range p.Children {
			walk(ch)
		}
	}
	walk(mime.ParseStructure(raw))
}

// handleRecover restores a message from Deleted Items back to the Inbox.
func (s *Server) handleRecover(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	if _, err := st.MoveMessage(fid, uid, mapi.PrivateFIDInbox); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "recover failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"folder": "inbox"})
}

// handleLabels sets a message's labels (stored as its categories).
func (s *Server) handleLabels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string   `json:"id"`
		Labels []string `json:"labels"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, fid, uid, ok := s.locate(w, r, req.ID)
	if !ok {
		return
	}
	defer st.Close()
	info, err := st.MessageByUID(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err := st.SetCategories(info.ID, req.Labels); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not set labels"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSearch scans the mail folders for messages matching the query, on
// subject/sender (and body when present).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	results := []mailJSON{}
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"emails": results, "total": 0, "query": q})
		return
	}
	for slug, fid := range searchFolders() {
		msgs, err := st.ListMessages(fid)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			hay := strings.ToLower(m.Subject + " " + m.Sender)
			if !strings.Contains(hay, q) {
				if raw, err := st.GetMessageRaw(fid, m.UID); err == nil {
					if root := mime.ParseStructure(raw); !strings.Contains(strings.ToLower(bestBody(root)), q) {
						continue
					}
				} else {
					continue
				}
			}
			results = append(results, mailJSON{
				ID: messageID(slug, m.UID), From: m.Sender, FromName: m.Sender,
				Subject: m.Subject, Date: m.InternalDate.Format("2006-01-02T15:04:05Z07:00"),
				Read: m.Flags&objectstore.FlagSeen != 0, Starred: m.Flags&objectstore.FlagFlagged != 0,
				Folder: slug, Size: int(m.Size),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"emails": results, "total": len(results), "query": q})
}

// searchFolders is the set of mail folders the search scans.
func searchFolders() map[string]int64 {
	return map[string]int64{
		"inbox":  mapi.PrivateFIDInbox,
		"sent":   mapi.PrivateFIDSentItems,
		"drafts": mapi.PrivateFIDDraft,
		"trash":  mapi.PrivateFIDDeletedItems,
		"spam":   mapi.PrivateFIDJunk,
	}
}

// handleImport stores an uploaded .eml (base64) into a folder of the caller's
// OWN mailbox (default Inbox), the same way AppendMessage imports delivered mail.
// Importing into a shared mailbox is not supported (mirrors webmail).
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	var req struct {
		File   string `json:"file"` // base64-encoded .eml
		Folder string `json:"folder"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "upload too large or malformed"})
		return
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.File))
	if err != nil || len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choose a valid .eml file"})
		return
	}
	mb, ok := s.openMailbox(w, r)
	if !ok {
		return
	}
	defer mb.st.Close()
	if mb.shared {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "import into a shared mailbox is not supported"})
		return
	}
	folder := req.Folder
	if folder == "" {
		folder = "inbox"
	}
	fid, ok := folderFID(folder)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown folder"})
		return
	}
	info, err := mb.st.AppendMessage(fid, raw, time.Now(), 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not import message"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": info.UID, "folder": folder})
}

// handleThreads returns the inbox grouped into simple subject threads.
// threadJSON is one conversation: its messages plus the derived header fields the
// SPA renders (participants, last activity, unread count). Messages carry full
// rows so the conversation view needs no extra fetch.
type threadJSON struct {
	Key          string     `json:"key"`
	Subject      string     `json:"subject"`
	Messages     []mailJSON `json:"messages"`
	Participants []string   `json:"participants"`
	LastDate     string     `json:"lastDate"`
	Unread       int        `json:"unread"`
}

// reThreadPrefix strips one or more leading Re:/Fwd:/Fw: prefixes; it mirrors the
// SPA's normalizeSubject regex so the grouping is identical on both sides.
var reThreadPrefix = regexp.MustCompile(`(?i)^(\s*(re|fwd|fw)\s*:\s*)+`)

// normalizeThreadSubject removes reply/forward prefixes so a reply groups with its
// original.
func normalizeThreadSubject(subject string) string {
	return strings.TrimSpace(reThreadPrefix.ReplaceAllString(subject, ""))
}

// groupThreads buckets messages by normalized subject (newest activity last within
// a bucket, since msgs arrives oldest-first), then orders buckets with the longest
// conversations first, the same grouping the SPA used to do client-side.
func groupThreads(folder string, msgs []objectstore.MessageInfo) []threadJSON {
	order := make([]string, 0)
	buckets := make(map[string][]mailJSON)
	for _, m := range msgs {
		key := strings.ToLower(normalizeThreadSubject(m.Subject))
		if key == "" {
			key = "(no subject)"
		}
		if _, seen := buckets[key]; !seen {
			order = append(order, key)
		}
		buckets[key] = append(buckets[key], mailRow(folder, m))
	}
	threads := make([]threadJSON, 0, len(order))
	for _, key := range order {
		rows := buckets[key]
		seen := make(map[string]bool, len(rows))
		participants := make([]string, 0, len(rows))
		unread := 0
		for _, row := range rows {
			if !seen[row.From] {
				seen[row.From] = true
				participants = append(participants, row.From)
			}
			if !row.Read {
				unread++
			}
		}
		threads = append(threads, threadJSON{
			Key:          key,
			Subject:      normalizeThreadSubject(rows[0].Subject),
			Messages:     rows,
			Participants: participants,
			LastDate:     rows[len(rows)-1].Date,
			Unread:       unread,
		})
	}
	sort.SliceStable(threads, func(i, j int) bool {
		return len(threads[i].Messages) > len(threads[j].Messages)
	})
	return threads
}

// handleThreads groups the inbox into conversations server-side (?owner targets a
// shared mailbox), so both the conversations view and the threads page render from
// one grouped response instead of regrouping the flat list in the browser.
func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	mb, ok := s.openMailbox(w, r)
	if !ok {
		return
	}
	defer mb.st.Close()
	fid, ok := resolveFolder(mb.st, "inbox")
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"threads": []threadJSON{}})
		return
	}
	if !mb.readAllowed(fid) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	msgs, err := mb.st.ListMessages(fid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"threads": groupThreads("inbox", msgs)})
}

// handleMarkAllRead marks every unread message in a folder \Seen, in the caller's
// own or a shared mailbox (?owner), and reports how many it changed.
func (s *Server) handleMarkAllRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string `json:"folder"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	mb, ok := s.openMailbox(w, r)
	if !ok {
		return
	}
	defer mb.st.Close()
	fid, ok := folderFID(req.Folder)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown folder"})
		return
	}
	if !mb.readAllowed(fid) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	msgs, err := mb.st.ListMessages(fid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot read folder"})
		return
	}
	marked := 0
	for _, m := range msgs {
		if m.Flags&objectstore.FlagSeen == 0 {
			if err := mb.st.SetMessageFlags(fid, m.UID, m.Flags|objectstore.FlagSeen); err == nil {
				marked++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"marked": marked})
}

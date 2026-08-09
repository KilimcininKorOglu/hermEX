package webmail2api

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"net/http"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// safeAttachmentName reduces a sender-supplied attachment filename to something
// safe to use as a zip entry name and inside a Content-Disposition header. The
// wire value is whatever the sender chose to write: Part.Filename is a faithful
// accessor for it and was never meant to yield a filesystem-safe name.
//
// Two sinks care. A zip entry named "../../../.ssh/authorized_keys" is written
// outside the extraction directory by any tool that honours relative paths, so an
// external sender would be choosing where files land on the recipient's machine.
// A quote inside a Content-Disposition parameter ends the quoted string early and
// hands the sender the rest of the header. Only the base name survives, and a name
// that reduces to nothing or to a dot segment falls back to the caller's own
// generated name.
func safeAttachmentName(raw, fallback string) string {
	// Senders send Windows paths too, and path.Base does not treat a backslash as
	// a separator.
	base := path.Base(strings.ReplaceAll(raw, "\\", "/"))
	if base == "" || base == "." || base == ".." || base == "/" {
		return fallback
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r < 0x20 || r == 0x7f:
			// drop control characters
		case r == '"':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
		if b.Len() >= 200 {
			break
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" || name == "." || name == ".." {
		return fallback
	}
	return name
}

// servedAttachmentType picks the Content-Type an attachment is served with.
//
// The declared type is whatever the sender wrote, and the reader decides how to
// render an attachment from it: an image/* preview goes in an <img>, an
// application/pdf preview in an <object>. Passing the declaration through unchecked
// lets the sender choose how their bytes are treated, so it is honoured only when it
// names one of those two AND the bytes themselves agree. An image is served as the
// type the bytes actually are, not the one the sender claimed.
//
// Everything else is served opaque, which is what the response's
// Content-Disposition already says it is. That is deliberately narrow: a format the
// sniffer cannot confirm (SVG, TIFF, HEIC) stops previewing rather than being
// rendered on the sender's word.
func servedAttachmentType(declared string, body []byte) string {
	const opaque = "application/octet-stream"
	sniffed, _, _ := strings.Cut(http.DetectContentType(body), ";")
	switch {
	case declared == "application/pdf" && sniffed == "application/pdf":
		return declared
	case strings.HasPrefix(declared, "image/") && strings.HasPrefix(sniffed, "image/"):
		return sniffed
	}
	return opaque
}

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
	filename := safeAttachmentName(found.Filename(), "attachment")
	w.Header().Set("Content-Type", servedAttachmentType(found.Type+"/"+found.Subtype, body))
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

// Search bounds. A search has no index behind it: it walks the folder listings
// and, for any filter that needs the message text, reads and parses the message
// itself. On a large mailbox an unbounded walk is a multi-second burst of disk
// reads and MIME parsing inside one interactive request, and it can answer with
// an arbitrarily large JSON array.
//
// maxSearchResults bounds the answer; maxSearchBodyReads bounds the expensive
// half, the per-message read and parse. Whichever is reached first stops the
// walk, and the reply says so rather than presenting a partial answer as whole.
const (
	maxSearchResults   = 200
	maxSearchBodyReads = 2000
)

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

// handleHeaders serves only a message's internet (RFC822) header block (the bytes
// up to the first blank line) as inline text/plain, for a "view internet headers"
// action distinct from the full source (own mailbox only, like handleSource).
func (s *Server) handleHeaders(w http.ResponseWriter, r *http.Request) {
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
	_, _ = w.Write(headerBlock(raw))
}

// headerBlock returns the RFC822 header section of raw: everything up to and
// including the CRLF (or LF) that ends the last header line, stopping at the blank
// line that separates headers from the body. A message with no blank line is all
// headers.
func headerBlock(raw []byte) []byte {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i+2]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i+1]
	}
	return raw
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
				fn := safeAttachmentName(p.Filename(), "attachment-"+strconv.Itoa(idx))
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

// handleSearch scans the mail folders for messages matching the query. The query
// is KQL-style: field:value filters (from/to/subject/body/category/has/is) plus
// general terms matched against subject/sender/body. Bare tokens preserve the
// legacy plain-text behaviour.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	raw := strings.TrimSpace(r.URL.Query().Get("q"))
	kql := parseKQL(raw)
	results := []mailJSON{}
	if raw == "" {
		writeJSON(w, http.StatusOK, map[string]any{"emails": results, "total": 0, "query": raw})
		return
	}
	bodyReads, truncated := 0, false
	ctx := r.Context()
scan:
	for _, f := range searchFolders() {
		slug, fid := f.slug, f.fid
		msgs, err := st.ListMessages(fid)
		if err != nil {
			continue
		}
		// Newest first. The listing is ordered by UID ascending, so walking it
		// forwards under a cap would keep the OLDEST matches, which is the wrong
		// end of the mailbox for a search.
		for _, m := range slices.Backward(msgs) {
			// A caller who navigated away must not leave the walk running.
			if ctx.Err() != nil {
				truncated = true
				break scan
			}
			if len(results) >= maxSearchResults || bodyReads >= maxSearchBodyReads {
				truncated = true
				break scan
			}
			sender := strings.ToLower(m.Sender)
			subject := strings.ToLower(m.Subject)
			// Field filters take precedence; each must hold for a match.
			if !containsAny(sender, kql.From) {
				continue
			}
			if !containsAny(subject, kql.Subject) {
				continue
			}
			if kql.Read != nil {
				isRead := m.Flags&objectstore.FlagSeen != 0
				if isRead != *kql.Read {
					continue
				}
			}
			// General terms and body/to filters need the raw MIME. Fetch once and
			// reuse for all of them.
			needRaw := len(kql.General) > 0 || len(kql.Body) > 0 || len(kql.To) > 0 || kql.HasAtt != nil
			var body, recipients string
			hasAtt := false
			if needRaw {
				bodyReads++
				rawMsg, err := st.GetMessageRaw(fid, m.UID)
				if err != nil {
					continue
				}
				root := mime.ParseStructure(rawMsg)
				body = strings.ToLower(bestBody(root))
				recipients = strings.ToLower(recipientsOf(root))
				hasAtt = mimeHasAttachment(root)
			}
			if len(kql.To) > 0 && !containsAny(recipients, kql.To) {
				continue
			}
			if len(kql.Body) > 0 && !containsAny(body, kql.Body) {
				continue
			}
			if kql.HasAtt != nil && hasAtt != *kql.HasAtt {
				continue
			}
			// General terms match against subject/sender/body together.
			if len(kql.General) > 0 {
				hay := subject + " " + sender + " " + body
				matched := false
				for _, term := range kql.General {
					if strings.Contains(hay, term) {
						matched = true
						break
					}
				}
				if !matched {
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
	writeJSON(w, http.StatusOK, map[string]any{
		"emails": results, "total": len(results), "query": raw,
		// The count is what was returned, not what exists: say plainly when the
		// walk stopped early so the caller can narrow instead of trusting a
		// partial answer.
		"truncated": truncated,
	})
}

// recipientsOf collects To/Cc/Bcc display strings from a MIME tree so the KQL
// "to:" filter can match addressees (the index's sender-only row misses them).
func recipientsOf(root *mime.Part) string {
	var b strings.Builder
	hdr := root.Header()
	for _, h := range []string{"To", "Cc", "Bcc"} {
		if v := hdr.Get(h); v != "" {
			b.WriteString(v)
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// mimeHasAttachment reports whether a MIME tree contains a part with
// Content-Disposition: attachment.
func mimeHasAttachment(p *mime.Part) bool {
	if p == nil {
		return false
	}
	if p.Disposition == "attachment" {
		return true
	}
	return slices.ContainsFunc(p.Children, mimeHasAttachment)
}

// searchFolders is the set of mail folders the search scans.
func searchFolders() []searchFolder {
	// The order is fixed and not incidental: a bounded scan stops partway on a
	// large mailbox, so which folders it reached has to be the same every time or
	// the same query would answer differently on each run. Inbox first, because
	// that is where a search is nearly always aimed.
	return []searchFolder{
		{"inbox", mapi.PrivateFIDInbox},
		{"sent", mapi.PrivateFIDSentItems},
		{"drafts", mapi.PrivateFIDDraft},
		{"spam", mapi.PrivateFIDJunk},
		{"trash", mapi.PrivateFIDDeletedItems},
	}
}

// searchFolder is one folder a search walks: the slug the SPA addresses it by
// and the well-known folder id.
type searchFolder struct {
	slug string
	fid  int64
}

// handleImport stores an uploaded .eml (base64) into a folder of the caller's
// OWN mailbox (default Inbox), the same way AppendMessage imports delivered mail.
// Importing into a shared mailbox is not supported (mirrors webmail).
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	// A tighter cap than the shared one, sharing its overflow state so an oversized
	// upload is still answered 413 rather than "malformed".
	if state := bodyState(r); state != nil {
		r.Body = boundedBody(w, r.Body, maxImportBytes, state)
	}
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
	// An imported .eml carries whatever the uploader put in it and never passes
	// through delivery, so it is scanned here or not at all.
	owner := ""
	if c, ok := s.session(r); ok {
		owner = c.Email
	}
	if mta.ScanStored(s.accounts, owner, "", raw, time.Now()) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the message was rejected: a virus was detected"})
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

// reThreadPrefix strips one or more leading Re:/Fwd:/Fw: prefixes. Grouping is
// server-side only: the SPA renders the threads this endpoint returns and derives
// nothing from the subject itself, so this regex has no client-side counterpart to
// stay in step with.
var reThreadPrefix = regexp.MustCompile(`(?i)^(\s*(re|fwd|fw)\s*:\s*)+`)

// normalizeThreadSubject removes reply/forward prefixes so a reply groups with its
// original.
func normalizeThreadSubject(subject string) string {
	return strings.TrimSpace(reThreadPrefix.ReplaceAllString(subject, ""))
}

// groupThreads buckets messages by normalized subject (newest activity last within
// a bucket, since msgs arrives oldest-first), then orders buckets with the longest
// conversations first. The SPA used to do this client-side and no longer does.
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
	// This is a bulk write, so it takes the write gate, not the read one: a
	// read-only grant on someone else's folder must not let the grantee clear
	// their unread state.
	if !mb.writeAllowed(fid) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	msgs, err := mb.st.ListMessages(fid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot read folder"})
		return
	}
	marked, failed := 0, 0
	for _, m := range msgs {
		if m.Flags&objectstore.FlagSeen == 0 {
			if err := mb.st.SetMessageFlags(fid, m.UID, m.Flags|objectstore.FlagSeen); err != nil {
				// A discarded error made a partial pass indistinguishable from a
				// folder that simply had less unread mail in it.
				failed++
				logError("mark-all-read", err, logging.Fields{"user": mb.user, "folder": req.Folder, "uid": m.UID})
				continue
			}
			marked++
		}
	}
	if failed > 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "some messages could not be marked", "marked": marked, "failed": failed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"marked": marked})
}

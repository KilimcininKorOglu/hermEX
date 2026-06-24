package webmail2api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// messageID encodes a folder slug and uid as the opaque id the SPA round-trips
// across read/flag/move/delete.
func messageID(folder string, uid uint32) string {
	return folder + ":" + strconv.FormatUint(uint64(uid), 10)
}

// parseMessageID splits a "<folder>:<uid>" message id back into its parts.
func parseMessageID(id string) (folder string, uid uint32, ok bool) {
	slug, num, found := strings.Cut(id, ":")
	if !found {
		return "", 0, false
	}
	n, err := strconv.ParseUint(num, 10, 32)
	if err != nil {
		return "", 0, false
	}
	return slug, uint32(n), true
}

// addrEmail renders an address as "local@host".
func addrEmail(a mime.Address) string {
	if a.Host == "" {
		return a.Mailbox
	}
	return a.Mailbox + "@" + a.Host
}

// addrEmails maps addresses to their "local@host" strings.
func addrEmails(as []mime.Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, addrEmail(a))
	}
	return out
}

// bestBody walks the MIME tree for the richest displayable body — text/html when
// present, else text/plain — returning the charset-decoded UTF-8 content. The SPA
// sanitizes it (DOMPurify) and renders with whitespace preserved, so a plain body
// also displays correctly.
func bestBody(root *mime.Part) string {
	var plain, html string
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil {
			return
		}
		if p.Type == "text" && p.Disposition != "attachment" {
			if c, err := p.DecodedContent(); err == nil {
				switch {
				case p.Subtype == "html" && html == "":
					html = string(c)
				case p.Subtype == "plain" && plain == "":
					plain = string(c)
				}
			}
		}
		for _, ch := range p.Children {
			walk(ch)
		}
	}
	walk(root)
	if html != "" {
		return html
	}
	return plain
}

// cidRef matches an <img> whose src is a cid: URL, capturing the tag text up to
// src= (1) and the bare Content-ID it references (2).
var cidRef = regexp.MustCompile(`(?i)(<img\b[^>]*?\bsrc=)["']cid:([^"'>\s]+)["']`)

// inlineCIDImages rewrites each cid: <img> src in the HTML body to the referenced
// part decoded into a data: URI, returning the rewritten body and the set of
// inlined Content-IDs (so they are dropped from the downloadable attachment list).
// An unresolvable or undecodable cid: is left untouched.
func inlineCIDImages(body string, root *mime.Part) (string, map[string]bool) {
	leaves := map[string]*mime.Part{}
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil {
			return
		}
		if len(p.Children) > 0 {
			for _, ch := range p.Children {
				walk(ch)
			}
			return
		}
		if id := strings.Trim(p.ID, "<>"); id != "" {
			leaves[id] = p
		}
	}
	walk(root)
	if len(leaves) == 0 {
		return body, nil
	}
	inlined := map[string]bool{}
	out := cidRef.ReplaceAllStringFunc(body, func(match string) string {
		sub := cidRef.FindStringSubmatch(match)
		part, ok := leaves[sub[2]]
		if !ok {
			return match
		}
		content, err := part.DecodedContent()
		if err != nil {
			return match
		}
		inlined[sub[2]] = true
		ct := part.Type + "/" + part.Subtype
		return sub[1] + `"data:` + ct + ";base64," + base64.StdEncoding.EncodeToString(content) + `"`
	})
	return out, inlined
}

// attachmentJSON is the SPA's AttachmentInfo shape.
type attachmentJSON struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
	Index       int    `json:"index"`
}

// collectAttachments walks the tree for non-inline attachment parts, assigning
// each a sequential index in walk order.
func collectAttachments(root *mime.Part, inlined map[string]bool) []attachmentJSON {
	var atts []attachmentJSON
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
		// An image already inlined into the body is not a separate download.
		cid := strings.Trim(p.ID, "<>")
		if cid != "" && inlined[cid] {
			for _, ch := range p.Children {
				walk(ch)
			}
			return
		}
		if p.Type != "multipart" && (p.Disposition == "attachment" || name != "") {
			atts = append(atts, attachmentJSON{
				Filename:    name,
				ContentType: p.Type + "/" + p.Subtype,
				Size:        p.Size,
				Index:       idx,
			})
			idx++
		}
		for _, ch := range p.Children {
			walk(ch)
		}
	}
	walk(root)
	return atts
}

// mailDetailJSON is the full Mail shape returned for a single message read.
type mailDetailJSON struct {
	ID             string           `json:"id"`
	From           string           `json:"from"`
	FromName       string           `json:"fromName"`
	To             []string         `json:"to"`
	Cc             []string         `json:"cc,omitempty"`
	Subject        string           `json:"subject"`
	Body           string           `json:"body"`
	Preview        string           `json:"preview"`
	Date           string           `json:"date"`
	Read           bool             `json:"read"`
	Starred        bool             `json:"starred"`
	Folder         string           `json:"folder"`
	HasAttachments bool             `json:"hasAttachments"`
	Size           int              `json:"size"`
	Attachments    []attachmentJSON `json:"attachments,omitempty"`
}

// handleMailMessage returns a single message's full detail and marks it read.
func (s *Server) handleMailMessage(w http.ResponseWriter, r *http.Request) {
	mb, ok := s.openMailbox(w, r)
	if !ok {
		return
	}
	defer mb.st.Close()
	folder, uid, ok := parseMessageID(r.URL.Query().Get("id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	fid, ok := folderFID(folder)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown folder"})
		return
	}
	if !mb.readAllowed(fid) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	st := mb.st
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	d := buildMailDetail(raw, folder, uid)

	// Reading marks the message \Seen, preserving its other flags.
	if flags, err := st.MessageFlags(fid, uid); err == nil {
		d.Read = flags&objectstore.FlagSeen != 0
		d.Starred = flags&objectstore.FlagFlagged != 0
		if flags&objectstore.FlagSeen == 0 && !mb.shared {
			_ = st.SetMessageFlags(fid, uid, flags|objectstore.FlagSeen)
			d.Read = true
		}
	}
	writeJSON(w, http.StatusOK, d)
}

// buildMailDetail builds a message's full detail JSON from its raw bytes: the
// richest body with inline cid: images resolved, the envelope fields, and the
// attachment list. It does NOT touch flags — the caller owns \Seen.
func buildMailDetail(raw []byte, folder string, uid uint32) mailDetailJSON {
	root := mime.ParseStructure(raw)
	body, inlined := inlineCIDImages(bestBody(root), root)
	d := mailDetailJSON{
		ID:      messageID(folder, uid),
		Subject: "(no subject)",
		Folder:  folder,
		Body:    body,
		Size:    len(raw),
	}
	if env, err := mime.ParseEnvelope(raw); err == nil {
		if env.Subject != "" {
			d.Subject = env.Subject
		}
		if len(env.From) > 0 {
			d.From = addrEmail(env.From[0])
			d.FromName = env.From[0].Name
			if d.FromName == "" {
				d.FromName = d.From
			}
		}
		d.To = addrEmails(env.To)
		d.Cc = addrEmails(env.Cc)
		if !env.Date.IsZero() {
			d.Date = env.Date.Format(time.RFC3339)
		}
	}
	d.Attachments = collectAttachments(root, inlined)
	d.HasAttachments = len(d.Attachments) > 0
	return d
}

// locate resolves a message id to its store, folder id, and uid for a mutating
// op, writing the error response and reporting false when anything fails. The
// caller closes the returned store.
func (s *Server) locate(w http.ResponseWriter, r *http.Request, id string) (*objectstore.Store, int64, uint32, bool) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil, 0, 0, false
	}
	folder, uid, ok := parseMessageID(id)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return nil, 0, 0, false
	}
	fid, ok := folderFID(folder)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown folder"})
		return nil, 0, 0, false
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return nil, 0, 0, false
	}
	return st, fid, uid, true
}

// handleMailFlag sets or clears a message's \Seen or \Flagged flag.
func (s *Server) handleMailFlag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Flag  string `json:"flag"`
		Value bool   `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	var bit int64
	switch req.Flag {
	case `\Seen`:
		bit = objectstore.FlagSeen
	case `\Flagged`:
		bit = objectstore.FlagFlagged
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown flag"})
		return
	}
	st, fid, uid, ok := s.locate(w, r, req.ID)
	if !ok {
		return
	}
	defer st.Close()
	cur, err := st.MessageFlags(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if req.Value {
		cur |= bit
	} else {
		cur &^= bit
	}
	if err := st.SetMessageFlags(fid, uid, cur); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "flag failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMailMove moves a message to another folder.
func (s *Server) handleMailMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	dst, ok := folderFID(req.To)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown target folder"})
		return
	}
	st, fid, uid, ok := s.locate(w, r, req.ID)
	if !ok {
		return
	}
	defer st.Close()
	if _, err := st.MoveMessage(fid, uid, dst); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "move failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMailDelete removes a message: from Trash it is deleted outright, from any
// other folder it is moved to Deleted Items (recoverable).
func (s *Server) handleMailDelete(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	var err error
	if fid == mapi.PrivateFIDDeletedItems {
		err = st.DeleteMessage(fid, uid)
	} else {
		_, err = st.MoveMessage(fid, uid, mapi.PrivateFIDDeletedItems)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

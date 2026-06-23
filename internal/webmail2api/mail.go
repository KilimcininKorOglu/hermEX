package webmail2api

import (
	"encoding/json"
	"net/http"
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

// attachmentJSON is the SPA's AttachmentInfo shape.
type attachmentJSON struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
	Index       int    `json:"index"`
}

// collectAttachments walks the tree for non-inline attachment parts, assigning
// each a sequential index in walk order.
func collectAttachments(root *mime.Part) []attachmentJSON {
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
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
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
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	root := mime.ParseStructure(raw)
	d := mailDetailJSON{
		ID:      messageID(folder, uid),
		Subject: "(no subject)",
		Folder:  folder,
		Body:    bestBody(root),
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
	d.Attachments = collectAttachments(root)
	d.HasAttachments = len(d.Attachments) > 0

	// Reading marks the message \Seen, preserving its other flags.
	if flags, err := st.MessageFlags(fid, uid); err == nil {
		d.Read = flags&objectstore.FlagSeen != 0
		d.Starred = flags&objectstore.FlagFlagged != 0
		if flags&objectstore.FlagSeen == 0 {
			_ = st.SetMessageFlags(fid, uid, flags|objectstore.FlagSeen)
			d.Read = true
		}
	}
	writeJSON(w, http.StatusOK, d)
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

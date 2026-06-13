package webmail

import (
	"encoding/base64"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// attachmentView is one downloadable attachment in the message view.
type attachmentView struct {
	Filename    string
	ContentType string
	Size        int
	Section     string // numeric part path, e.g. "2"
}

// messageDetail is the data the message template renders.
type messageDetail struct {
	UID         uint32
	Folder      string
	From        string
	To          string
	Cc          string
	Subject     string
	Date        string
	IsHTML      bool
	Body        string
	Attachments []attachmentView
	Folders     []folderView // move/copy targets (mail folders except this one)
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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

	detail := buildMessageDetail(raw, folder, uid)
	detail.Folders = moveTargets(folders, folderID)

	// Reading a message marks it \Seen (read-modify-write to preserve the rest).
	if cur, err := st.MessageFlags(folderID, uid); err == nil && cur&objectstore.FlagSeen == 0 {
		st.SetMessageFlags(folderID, uid, cur|objectstore.FlagSeen)
	}

	s.render(w, "message", detail)
}

// buildMessageDetail parses a raw message into the message view, selecting the
// best displayable body and listing attachments.
func buildMessageDetail(raw []byte, folder string, uid uint32) messageDetail {
	root := mime.ParseStructure(raw)
	d := messageDetail{UID: uid, Folder: folder, Subject: "(no subject)"}
	if env, err := mime.ParseEnvelope(raw); err == nil {
		if env.Subject != "" {
			d.Subject = env.Subject
		}
		d.From = formatAddrs(env.From)
		d.To = formatAddrs(env.To)
		d.Cc = formatAddrs(env.Cc)
		if !env.Date.IsZero() {
			d.Date = env.Date.Format("2006-01-02 15:04")
		} else {
			d.Date = env.RawDate
		}
	}

	bodyPart, isHTML, atts := selectParts(root)
	d.IsHTML = isHTML
	if bodyPart != nil {
		if content, err := bodyPart.DecodedContent(); err == nil {
			d.Body = toUTF8(content, bodyPart.Params["charset"])
		}
	}
	// Inline images: replace each cid: reference in the HTML body with the
	// referenced part inlined as a data: URI, and drop those parts from the
	// attachment list so they render in place rather than as downloads. Inlining
	// keeps the reader iframe fully sandboxed — no credentialed subresource that
	// received (untrusted) HTML could aim at any other endpoint — and reuses the
	// self-contained data: form already used for inline images in a saved draft.
	if isHTML && d.Body != "" {
		var inlined map[string]bool
		d.Body, inlined = inlineCIDImages(d.Body, root)
		atts = dropSections(atts, inlined)
	}
	d.Attachments = atts
	if d.Body == "" && !isHTML {
		d.Body = "(no displayable text body)"
	}
	return d
}

// cidRef matches an <img> whose src is a cid: URL, capturing the tag text up to
// src= (1) and the bare Content-ID it references (2).
var cidRef = regexp.MustCompile(`(?i)(<img\b[^>]*?\bsrc=)["']cid:([^"'>\s]+)["']`)

// cidLeaf is a leaf part addressable by its Content-ID, with the numeric section
// path that locates it in the message.
type cidLeaf struct {
	part    *mime.Part
	section string
}

// cidLeaves maps each leaf part's bare Content-ID to the part and its section
// path, mirroring selectParts' path assignment so the two stay consistent.
func cidLeaves(root *mime.Part) map[string]cidLeaf {
	m := map[string]cidLeaf{}
	var walk func(p *mime.Part, path []int)
	walk = func(p *mime.Part, path []int) {
		if len(p.Children) > 0 {
			for i, ch := range p.Children {
				walk(ch, append(append([]int{}, path...), i+1))
			}
			return
		}
		if id := strings.Trim(p.ID, "<>"); id != "" {
			m[id] = cidLeaf{part: p, section: pathString(path)}
		}
	}
	walk(root, nil)
	return m
}

// inlineCIDImages rewrites each cid: <img> source in an HTML body to the
// referenced part decoded into a data: URI, returning the rewritten body and the
// set of section paths that were inlined (so the caller can drop them from the
// attachment list). An unresolvable or undecodable cid: is left untouched.
func inlineCIDImages(body string, root *mime.Part) (string, map[string]bool) {
	leaves := cidLeaves(root)
	if len(leaves) == 0 {
		return body, nil
	}
	inlined := map[string]bool{}
	out := cidRef.ReplaceAllStringFunc(body, func(match string) string {
		sub := cidRef.FindStringSubmatch(match)
		leaf, ok := leaves[sub[2]]
		if !ok {
			return match
		}
		content, err := leaf.part.DecodedContent()
		if err != nil {
			return match
		}
		inlined[leaf.section] = true
		ct := leaf.part.Type + "/" + leaf.part.Subtype
		return sub[1] + `"data:` + ct + ";base64," + base64.StdEncoding.EncodeToString(content) + `"`
	})
	return out, inlined
}

// dropSections returns the attachments whose section path is not in drop, used to
// hide inline images (rendered in the body) from the downloadable list.
func dropSections(atts []attachmentView, drop map[string]bool) []attachmentView {
	if len(drop) == 0 {
		return atts
	}
	out := make([]attachmentView, 0, len(atts))
	for _, a := range atts {
		if !drop[a.Section] {
			out = append(out, a)
		}
	}
	return out
}

// selectParts walks the MIME tree, choosing a display body (HTML preferred over
// plain text) and collecting attachments. Each attachment carries its numeric
// section path for the download link.
func selectParts(root *mime.Part) (body *mime.Part, isHTML bool, atts []attachmentView) {
	var html, plain *mime.Part
	var walk func(p *mime.Part, path []int)
	walk = func(p *mime.Part, path []int) {
		if len(p.Children) > 0 {
			for i, ch := range p.Children {
				childPath := append(append([]int{}, path...), i+1)
				walk(ch, childPath)
			}
			return
		}
		isText := p.Type == "text" && p.Disposition != "attachment"
		switch {
		case isText && p.Subtype == "html" && html == nil:
			html = p
		case isText && p.Subtype == "plain" && plain == nil:
			plain = p
		default:
			atts = append(atts, attachmentView{
				Filename:    attachmentName(p),
				ContentType: p.Type + "/" + p.Subtype,
				Size:        p.Size,
				Section:     pathString(path),
			})
		}
	}
	walk(root, nil)

	if html != nil {
		return html, true, atts
	}
	return plain, false, atts
}

// attachmentName returns a display name for an attachment, falling back to a
// generic name keyed by type when none is given.
func attachmentName(p *mime.Part) string {
	if fn := p.Filename(); fn != "" {
		return fn
	}
	return "attachment." + p.Subtype
}

// pathString renders a numeric part path as an IMAP-style "1.2" string.
func pathString(path []int) string {
	parts := make([]string, len(path))
	for i, n := range path {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// formatAddrs renders an address list for header display.
func formatAddrs(addrs []mime.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		addr := a.Mailbox + "@" + a.Host
		if a.Name != "" {
			parts = append(parts, a.Name+" <"+addr+">")
		} else {
			parts = append(parts, addr)
		}
	}
	return strings.Join(parts, ", ")
}

// toUTF8 converts a decoded body to a UTF-8 string per its charset, delegating
// to the shared MIME charset converter.
func toUTF8(b []byte, charset string) string {
	return mime.DecodeCharset(b, charset)
}

package webmail

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mime"
	"hermex/internal/store"
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

	st, err := store.Open(sess.mailboxPath)
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

	// Reading a message marks it \Seen (read-modify-write to preserve the rest).
	if cur, err := st.MessageFlags(folderID, uid); err == nil && cur&store.FlagSeen == 0 {
		st.SetMessageFlags(folderID, uid, cur|store.FlagSeen)
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
	d.Attachments = atts
	if bodyPart != nil {
		if content, err := bodyPart.DecodedContent(); err == nil {
			d.Body = toUTF8(content, bodyPart.Params["charset"])
		}
	}
	if d.Body == "" && !isHTML {
		d.Body = "(no displayable text body)"
	}
	return d
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

// toUTF8 converts a decoded body to a UTF-8 string per its charset. UTF-8 and
// US-ASCII pass through; Latin-1 family bytes map to runes; unknown charsets
// are treated as UTF-8 on a best-effort basis.
func toUTF8(b []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "iso8859-1", "windows-1252", "cp1252":
		runes := make([]rune, len(b))
		for i, c := range b {
			runes[i] = rune(c)
		}
		return string(runes)
	default:
		return string(b)
	}
}

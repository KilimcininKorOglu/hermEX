package webmail

import (
	"crypto/x509"
	"encoding/base64"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
	"hermex/internal/smime"
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
	UID           uint32
	Folder        string
	From          string
	To            string
	Cc            string
	Subject       string
	Date          string
	IsHTML        bool
	Body          string
	RemoteBlocked bool // HTML message whose remote content (images/fonts) the CSP blocked; the reader offers a one-time "Show images"
	Attachments   []attachmentView
	Folders       []folderView   // move/copy targets (mail folders except this one)
	FlagColor     int32          // follow-up flag color 1-6 (0 none), shown in the reader header
	FlagComplete  bool           // follow-up flag marked complete
	FlagDue       string         // formatted follow-up due date, empty when none
	Categories    []categoryView // categories assigned to this message, with colors
	AllCategories []category     // the mailbox's master category list, for the assign control
	Preview       bool           // rendered inside the reading pane (partial, no page chrome) (#34)
	Importance    string         // "High" | "Low" label for the print header, "" when Normal/absent (#34)
	Sensitivity   string         // "Personal" | "Private" | "Confidential" for the print header, "" when Normal (#34)
	Smime         string         // S/MIME status banner text ("" when not an S/MIME message) (#41)
	SmimeOK       bool           // true when verified/decrypted (positive banner), false for a warning
	// Mbox is the shared mailbox address when the message belongs to one, else
	// empty. When set the attachment and action links carry &mbox={{.Mbox}}
	// (template-escaped). Reply/forward honor send-as (gated by CanSendAs);
	// print/eml stay own-mailbox only.
	Mbox string
	// ReadOnly hides the in-mailbox write controls (flag/move/categorize) — true
	// for a shared message the caller may read but not modify; false for the own
	// mailbox, so its reader is unaffected.
	ReadOnly bool
	// CanSendAs reports that the caller may reply to/forward this message as the
	// shared mailbox it belongs to (owner or delegate), so the reader offers the
	// compose controls wired to send from the shared address; false for the own
	// mailbox (where the controls show unconditionally) and for a shared message
	// the caller may read but not send as.
	CanSendAs bool
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
	preview := r.URL.Query().Get("mode") == "preview"

	// Open the own mailbox, or a shared mailbox the caller selected (?mbox),
	// validated and access-checked server-side. A shared message is shown read-only.
	mbox := mboxParam(r)
	var st *objectstore.Store
	if mbox == "" {
		if st, err = objectstore.Open(sess.mailboxPath); err != nil {
			http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
			return
		}
	} else {
		var addr string
		var ok bool
		if st, addr, ok = s.openSharedFor(sess, mbox); !ok {
			http.NotFound(w, r)
			return
		}
		mbox = addr
	}
	defer st.Close()

	// Settings drive both the body render style (force plain text) and the master
	// category list used to color badges; load once and reuse.
	cfg, err := loadSettings(st)
	if err != nil {
		cfg = defaultSettings()
	}

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
	// A shared folder must grant the caller read access; whether they may also write
	// (edit/delete) decides if the reader shows its in-mailbox action controls.
	readOnly := mbox != ""
	if mbox != "" {
		rights, err := st.ResolvePermission(folderID, sess.user)
		if err != nil || rights&mapi.FrightsReadAny == 0 {
			http.NotFound(w, r)
			return
		}
		readOnly = rights&(mapi.FrightsEditAny|mapi.FrightsDeleteAny) == 0
	}
	raw, err := st.GetMessageRaw(folderID, uid)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// S/MIME: verify a signature and/or decrypt before rendering, so the reader
	// shows the real content with a trust banner rather than the opaque container.
	displayRaw, smimeStatus, smimeOK := s.openSmime(sess, raw)

	detail := buildMessageDetail(displayRaw, folder, uid, cfg.IncomingRender == "plain", cfg.SafeSenders, r.URL.Query().Get("images") == "1")
	detail.Smime = smimeStatus
	detail.SmimeOK = smimeOK
	detail.Mbox = mbox
	detail.ReadOnly = readOnly
	// A shared message the caller may send as (owner or delegate) offers the
	// reply/forward controls, wired to compose from the shared address; a reviewer
	// who can only read sees no compose controls. The own mailbox shows them
	// unconditionally, so CanSendAs stays false there.
	detail.CanSendAs = mbox != "" && canSendAsShared(st, sess.user)
	// Move targets are offered only where the caller may write: the own mailbox, or
	// a shared folder they hold edit/delete rights on.
	if !readOnly {
		detail.Folders = moveTargets(folders, folderID)
	}

	// Reading a message marks it \Seen (read-modify-write to preserve the rest);
	// the \Flagged bit also feeds the follow-up flag fallback below. A shared
	// message is not marked read here — that is a write into the shared store.
	flagged := false
	if cur, err := st.MessageFlags(folderID, uid); err == nil {
		flagged = cur&objectstore.FlagFlagged != 0
		if mbox == "" && cur&objectstore.FlagSeen == 0 {
			st.SetMessageFlags(folderID, uid, cur|objectstore.FlagSeen)
		}
	}
	cats := cfg.Categories
	detail.AllCategories = cats
	if m, err := st.MessageByUID(folderID, uid); err == nil {
		if f, err := st.GetFollowupFlag(m.ID); err == nil {
			detail.FlagComplete = f.Status == objectstore.FlagStatusComplete
			switch {
			case f.Color > 0:
				detail.FlagColor = f.Color
			case !detail.FlagComplete && flagged:
				detail.FlagColor = objectstore.FlagColorRed
			}
			if !f.DueBy.IsZero() {
				detail.FlagDue = f.DueBy.Format("2006-01-02 15:04")
			}
		}
		if names, err := st.GetCategories(m.ID); err == nil {
			for _, n := range names {
				detail.Categories = append(detail.Categories, categoryView{Name: n, Color: catColor(cats, n)})
			}
		}
	}

	detail.Preview = preview
	if preview {
		s.render(w, "messagepreview", detail)
		return
	}
	s.render(w, "message", detail)
}

// openSmime prepares a message for display: a signed message is verified and an
// encrypted message is decrypted with the session's unlocked identity. It returns
// a renderable message — the inner content spliced under the original identity
// headers — plus a status banner and whether it is a positive (verified/decrypted)
// result. A message that is not S/MIME is returned unchanged with no banner; one
// that cannot be opened is returned unchanged with a warning banner.
func (s *Server) openSmime(sess *session, raw []byte) (display []byte, status string, ok bool) {
	switch {
	case smime.IsEncrypted(raw):
		if sess.smimeKey == nil || sess.smimeCert == nil {
			return raw, "Encrypted message. Unlock your certificate on the Certificates page to read it.", false
		}
		inner, err := smime.Decrypt(raw, sess.smimeCert, sess.smimeKey)
		if err != nil {
			s.smimeEvent(logging.LevelWarn, sess.user, "smime.decrypt", err.Error(), nil)
			return raw, "Encrypted message — it could not be decrypted with your certificate.", false
		}
		s.smimeEvent(logging.LevelInfo, sess.user, "smime.decrypt", "", nil)
		identity, _ := splitForSmime(raw)
		if smime.IsSigned(inner) {
			signer, content, verr := smime.Verify(inner)
			if verr != nil {
				s.smimeEvent(logging.LevelWarn, sess.user, "smime.verify", verr.Error(), nil)
				return spliceIdentity(identity, inner), "Encrypted, but the signature could not be verified.", false
			}
			sigStatus, trusted := signerStatus(signer, raw)
			s.smimeEvent(logging.LevelInfo, sess.user, "smime.verify", "", logging.Fields{"signer": signer.Subject.CommonName, "trusted": trusted})
			return spliceIdentity(identity, content), "Encrypted. " + sigStatus, trusted
		}
		return spliceIdentity(identity, inner), "Encrypted — decrypted with your certificate.", true
	case smime.IsSigned(raw):
		signer, content, err := smime.Verify(raw)
		if err != nil {
			s.smimeEvent(logging.LevelWarn, sess.user, "smime.verify", err.Error(), nil)
			return raw, "Signed message — the signature could NOT be verified.", false
		}
		identity, _ := splitForSmime(raw)
		status, trusted := signerStatus(signer, raw)
		s.smimeEvent(logging.LevelInfo, sess.user, "smime.verify", "", logging.Fields{"signer": signer.Subject.CommonName, "trusted": trusted})
		return spliceIdentity(identity, content), status, trusted
	default:
		return raw, "", false
	}
}

// signerStatus produces an honest banner for a cryptographically valid signature.
// A valid signature proves only that the holder of the signer certificate's
// private key produced it — NOT that the certificate is trusted (no CA chain or
// TOFU anchor is checked here) and NOT, on its own, that the sender is genuine.
// So the banner says "Signed by <signer>" rather than "verified", and binds the
// signer to the envelope: if the certificate's email does not match the From
// address, that is reported as a warning (a self-signed certificate minted with
// the victim's address is the cheap spoof this catches). Full chain/TOFU
// validation is deliberately left as future work; the wording must not promise
// more trust than was established.
func signerStatus(signer *x509.Certificate, raw []byte) (status string, ok bool) {
	who := signerEmail(signer)
	from := fromAddress(raw)
	if from != "" && !strings.EqualFold(who, from) {
		return "Signed by " + who + ", which does NOT match the sender (" + from + ").", false
	}
	return "Signed by " + who + " (signature valid; certificate not checked against a trusted authority).", true
}

// signerEmail is the address a certificate speaks for: the first rfc822Name SAN
// if present, otherwise the subject common name. Used to bind a signer to the
// message's From address.
func signerEmail(cert *x509.Certificate) string {
	if cert == nil {
		return "an unknown signer"
	}
	if len(cert.EmailAddresses) > 0 {
		return cert.EmailAddresses[0]
	}
	if cert.Subject.CommonName != "" {
		return cert.Subject.CommonName
	}
	return "an unnamed certificate"
}

// fromAddress extracts the bare From address (lowercased) from a raw message, or
// "" when it cannot be parsed.
func fromAddress(raw []byte) string {
	env, err := mime.ParseEnvelope(raw)
	if err != nil || len(env.From) == 0 {
		return ""
	}
	return strings.ToLower(env.From[0].Mailbox + "@" + env.From[0].Host)
}

// buildMessageDetail parses a raw message into the message view, selecting the
// best displayable body and listing attachments. When preferPlain is set (the
// "display incoming mail as plain text" preference) a text/plain alternative is
// chosen over HTML, and an HTML-only message is down-converted to text so the
// reader never shows raw markup. safeSenders gates remote content in the HTML
// body: unless the sender is allow-listed, a restrictive CSP meta is injected so
// the reader loads no remote images (tracking pixels) or other subresources.
func buildMessageDetail(raw []byte, folder string, uid uint32, preferPlain bool, safeSenders []string, allowImages bool) messageDetail {
	root := mime.ParseStructure(raw)
	d := messageDetail{UID: uid, Folder: folder, Subject: "(no subject)"}
	var senderAddr string
	if env, err := mime.ParseEnvelope(raw); err == nil {
		if env.Subject != "" {
			d.Subject = env.Subject
		}
		if len(env.From) > 0 {
			senderAddr = env.From[0].Mailbox + "@" + env.From[0].Host
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

	bodyPart, isHTML, atts := selectParts(root, preferPlain)
	d.IsHTML = isHTML
	if bodyPart != nil {
		if content, err := bodyPart.DecodedContent(); err == nil {
			d.Body = toUTF8(content, bodyPart.Params["charset"])
		}
	}
	// Forced plain view of an HTML-only message: down-convert the markup to text
	// (selectParts already returned the text/plain part when one existed, so this
	// only fires when HTML is all there is). Done before the inline-CID step below,
	// which is then skipped — there is no markup left to host inline images.
	if isHTML && preferPlain {
		d.Body = htmlToText(d.Body)
		d.IsHTML, isHTML = false, false
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
	// Remote-content policy: prepend a CSP meta into the document the reader
	// iframe loads. Unless the sender is allow-listed, remote subresources are
	// forbidden, so tracking pixels and remote images never load; inline (data:)
	// images and inline styles still render. The iframe's sandbox blocks scripts;
	// this is the complementary layer sandbox does not cover. As the first node in
	// srcdoc the meta is foster-parented into the document head and applies to the
	// whole body, and a body's own CSP can only intersect (most-restrictive wins).
	if d.IsHTML {
		// allowImages is the per-view override (a "Show images" click); a safe
		// sender is allowed remote content by standing policy. RemoteBlocked drives
		// the reader's one-time "Show images" affordance, shown only when something
		// was actually withheld.
		allowRemote := allowImages || isSafeSender(safeSenders, senderAddr)
		d.Body = remoteContentMeta(allowRemote) + d.Body
		d.RemoteBlocked = !allowRemote
	}
	return d
}

// remoteContentMeta returns the Content-Security-Policy <meta> tag for the reader
// iframe's document. When the sender is not allow-listed, remote subresources are
// forbidden (only data: images and inline styles render); when allow-listed,
// remote images and fonts are permitted too. Scripts, objects, and frames are
// forbidden in both modes via default-src 'none', complementing the sandbox.
func remoteContentMeta(allowRemote bool) string {
	csp := "default-src 'none'; img-src data:; style-src 'unsafe-inline'"
	if allowRemote {
		csp = "default-src 'none'; img-src * data:; style-src 'unsafe-inline'; font-src * data:"
	}
	return `<meta http-equiv="Content-Security-Policy" content="` + csp + `">`
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
var (
	// htmlDropBlocks removes <script>/<style> elements with their content.
	htmlDropBlocks = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(script|style)\s*>`)
	// htmlLineBreaks turns block boundaries into newlines so the text keeps its
	// paragraph structure once the tags are stripped.
	htmlLineBreaks = regexp.MustCompile(`(?i)<(br\s*/?|/p|/div|/tr|/li|/h[1-6]|/blockquote)\s*>`)
	// htmlAnyTag matches any remaining tag for removal.
	htmlAnyTag = regexp.MustCompile(`(?s)<[^>]+>`)
	// htmlManyBlankLines collapses three-or-more newlines into a single blank line.
	htmlManyBlankLines = regexp.MustCompile(`\n{3,}`)
)

// htmlToText converts an HTML body to a readable plain-text approximation for the
// "display incoming mail as plain text" preference: script/style content is
// dropped, block boundaries become newlines, remaining tags are removed, and HTML
// entities are unescaped. It is a display down-convert, not a faithful renderer.
func htmlToText(s string) string {
	s = htmlDropBlocks.ReplaceAllString(s, "")
	s = htmlLineBreaks.ReplaceAllString(s, "\n")
	s = htmlAnyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t\r")
	}
	s = strings.Join(lines, "\n")
	s = htmlManyBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// selectParts walks the MIME tree and picks the body part to display plus the
// attachment list. HTML wins by default; when preferPlain is set a text/plain
// alternative is chosen instead, and only when none exists does it fall back to
// the HTML part (which the caller then down-converts to text).
func selectParts(root *mime.Part, preferPlain bool) (body *mime.Part, isHTML bool, atts []attachmentView) {
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

	if preferPlain && plain != nil {
		return plain, false, atts
	}
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

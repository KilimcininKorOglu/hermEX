package webmail

import (
	"bytes"
	"fmt"
	"html"
	"net/mail"
	"regexp"
	"strings"

	"hermex/internal/mime"
)

// buildComposeFromSource prefills the compose view for a reply/forward action
// on a source message. self is the logged-in user's address, excluded from
// reply-all recipients. The behavior (Re:/Fwd: prefixing, quoted body,
// In-Reply-To/References linkage, reply-all = original To+Cc minus self) is
// grounded in the internal spec §4.
func buildComposeFromSource(action, folder string, uid uint32, raw []byte, self string, includeOriginal bool) composeView {
	env, err := mime.ParseEnvelope(raw)
	if err != nil {
		return composeView{Title: "New message"}
	}
	text := bestTextBody(raw)

	switch action {
	case "reply", "replyall":
		// The user may choose not to quote the original on a reply; the linkage
		// (In-Reply-To/References) and recipients are kept regardless.
		body := ""
		if includeOriginal {
			body = quoteForReply(env, text)
		}
		v := composeView{
			Title:      "Reply",
			To:         formatAddrs(env.ReplyTo),
			Subject:    ensureSubjectPrefix("Re:", env.Subject),
			Body:       body,
			InReplyTo:  env.MessageID,
			References: buildReferences(raw, env.MessageID),
		}
		if action == "replyall" {
			v.Title = "Reply all"
			v.Cc = formatAddrs(replyAllCc(env, self))
		}
		return v

	case "forward":
		return composeView{
			Title:   "Forward",
			Subject: ensureSubjectPrefix("Fwd:", env.Subject),
			Body:    quoteForForward(env, text),
		}

	case "forwardasattach":
		return composeView{
			Title:        "Forward as attachment",
			Subject:      ensureSubjectPrefix("Fwd:", env.Subject),
			AttachFolder: folder,
			AttachUID:    fmt.Sprint(uid),
		}

	case "editasnew":
		return composeView{
			Title:   "Edit as new",
			To:      formatAddrs(env.To),
			Cc:      formatAddrs(env.Cc),
			Subject: env.Subject,
			Body:    text,
		}

	case "editdraft":
		// Reopening a saved draft: prefill every field (including Bcc) and carry
		// the draft's location so a re-save replaces this same draft rather than
		// accumulating copies. A draft is the user's own content, so its HTML body
		// is trusted and reopened in HTML mode with its markup intact (the editor
		// pastes it the same way it restores an in-progress compose on error). The
		// draft's own format is set here so it wins over the user's compose default.
		v := composeView{
			Title:       "Edit draft",
			To:          formatAddrs(env.To),
			Cc:          formatAddrs(env.Cc),
			Bcc:         formatAddrs(env.Bcc),
			Subject:     env.Subject,
			Body:        text,
			DraftFolder: folder,
			DraftUID:    fmt.Sprint(uid),
		}
		if h := bestHTMLBody(raw); h != "" {
			v.Format, v.BodyHTML = "html", h
		} else {
			v.Format = "plain"
		}
		// Show the draft's existing attachments; the submit path re-reads them
		// from the store (the browser cannot re-populate a file input).
		v.Attachments = draftAttachments(raw)
		return v

	default:
		return composeView{Title: "New message"}
	}
}

// ensureSubjectPrefix prepends prefix ("Re:"/"Fwd:") unless the subject already
// carries it (case-insensitive), so replies to replies don't stack prefixes.
func ensureSubjectPrefix(prefix, subject string) string {
	s := strings.TrimSpace(subject)
	if s == "" {
		return strings.TrimSpace(prefix)
	}
	if strings.HasPrefix(strings.ToLower(s), strings.ToLower(prefix)) {
		return s
	}
	return prefix + " " + s
}

// replyAllCc computes the reply-all Cc list: the original To and Cc recipients,
// minus the logged-in user and minus the reply-to addresses already in To.
func replyAllCc(env *mime.Envelope, self string) []mime.Address {
	seen := map[string]bool{strings.ToLower(strings.TrimSpace(self)): true}
	for _, a := range env.ReplyTo {
		seen[addrKey(a)] = true
	}
	var out []mime.Address
	for _, a := range append(append([]mime.Address{}, env.To...), env.Cc...) {
		k := addrKey(a)
		if k == "@" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	return out
}

// addrKey is the case-insensitive mailbox@host identity used to dedupe and
// exclude recipients.
func addrKey(a mime.Address) string {
	return strings.ToLower(a.Mailbox + "@" + a.Host)
}

// quoteForReply builds the quoted reply body: a blank line, an attribution
// line, then the original text prefixed with "> ".
func quoteForReply(env *mime.Envelope, text string) string {
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(replyAttribution(env))
	b.WriteString("\n")
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// replyAttribution renders the "On <date>, <sender> wrote:" line.
func replyAttribution(env *mime.Envelope) string {
	who := "someone"
	if len(env.From) > 0 {
		who = displayAddr(env.From[0])
	}
	when := env.RawDate
	if !env.Date.IsZero() {
		when = env.Date.Format("2006-01-02 15:04")
	}
	if when != "" {
		return fmt.Sprintf("On %s, %s wrote:", when, who)
	}
	return fmt.Sprintf("%s wrote:", who)
}

// quoteForForward builds the inline-forward body: a forwarded-message banner
// with the original headers, then the original text.
func quoteForForward(env *mime.Envelope, text string) string {
	var b strings.Builder
	b.WriteString("\n\n---------- Forwarded message ----------\n")
	if len(env.From) > 0 {
		fmt.Fprintf(&b, "From: %s\n", formatAddrs(env.From))
	}
	when := env.RawDate
	if !env.Date.IsZero() {
		when = env.Date.Format("2006-01-02 15:04")
	}
	if when != "" {
		fmt.Fprintf(&b, "Date: %s\n", when)
	}
	fmt.Fprintf(&b, "Subject: %s\n", env.Subject)
	if len(env.To) > 0 {
		fmt.Fprintf(&b, "To: %s\n", formatAddrs(env.To))
	}
	b.WriteString("\n")
	b.WriteString(strings.ReplaceAll(text, "\r\n", "\n"))
	return b.String()
}

// displayAddr renders a single address for an attribution/banner line.
func displayAddr(a mime.Address) string {
	if a.Name != "" {
		return a.Name
	}
	return a.Mailbox + "@" + a.Host
}

// buildReferences computes the reply's References header per RFC 5322 §3.6.4:
// the parent's References (if any) followed by the parent's Message-ID.
func buildReferences(raw []byte, messageID string) string {
	refs := ""
	if msg, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil {
		refs = strings.TrimSpace(msg.Header.Get("References"))
	}
	switch {
	case refs == "" && messageID == "":
		return ""
	case refs == "":
		return messageID
	case messageID == "":
		return refs
	default:
		return refs + " " + messageID
	}
}

// bestTextBody extracts the most quotable plain-text rendering of a message:
// the text/plain part if present, else the text/html part stripped of tags.
// Faithful HTML quoting is handled with the rich-text compose work; this is the
// plain-text fallback used for quoting.
func bestTextBody(raw []byte) string {
	root := mime.ParseStructure(raw)
	var plain, htmlp *mime.Part
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if len(p.Children) > 0 {
			for _, c := range p.Children {
				walk(c)
			}
			return
		}
		if p.Type != "text" || p.Disposition == "attachment" {
			return
		}
		if p.Subtype == "plain" && plain == nil {
			plain = p
		}
		if p.Subtype == "html" && htmlp == nil {
			htmlp = p
		}
	}
	walk(root)
	if plain != nil {
		if c, err := plain.DecodedContent(); err == nil {
			return toUTF8(c, plain.Params["charset"])
		}
	}
	if htmlp != nil {
		if c, err := htmlp.DecodedContent(); err == nil {
			return stripHTML(toUTF8(c, htmlp.Params["charset"]))
		}
	}
	return ""
}

// bestHTMLBody returns the message's text/html part decoded to UTF-8, or "" when
// there is none. Unlike bestTextBody it returns the markup verbatim, for
// reopening a saved HTML draft in the editor with its formatting intact. This is
// only ever called on the user's own draft — received HTML is never seeded into
// the editor (it is quoted as escaped plain text instead).
func bestHTMLBody(raw []byte) string {
	root := mime.ParseStructure(raw)
	var htmlp *mime.Part
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if len(p.Children) > 0 {
			for _, c := range p.Children {
				walk(c)
			}
			return
		}
		if p.Type != "text" || p.Disposition == "attachment" {
			return
		}
		if p.Subtype == "html" && htmlp == nil {
			htmlp = p
		}
	}
	walk(root)
	if htmlp != nil {
		if c, err := htmlp.DecodedContent(); err == nil {
			return toUTF8(c, htmlp.Params["charset"])
		}
	}
	return ""
}

var (
	blockTagRE  = regexp.MustCompile(`(?is)<br\s*/?>|</p>|</div>|</tr>|</h[1-6]>`)
	scriptTagRE = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	anyTagRE    = regexp.MustCompile(`(?s)<[^>]+>`)
	manyNLRE    = regexp.MustCompile(`\n{3,}`)
)

// stripHTML converts an HTML body to a best-effort plain-text rendering for
// quoting: block-closing tags become newlines, script/style and remaining tags
// are removed, and entities are unescaped.
func stripHTML(s string) string {
	s = blockTagRE.ReplaceAllString(s, "\n")
	s = scriptTagRE.ReplaceAllString(s, "")
	s = anyTagRE.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = manyNLRE.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

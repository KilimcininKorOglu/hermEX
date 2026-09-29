package oxcmail

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"

	"hermex/internal/mapi"
	"hermex/internal/mime"
)

// hiddenElements hold no text a reader sees: their content is script, style or
// document metadata.
var hiddenElements = map[string]bool{
	"head": true, "script": true, "style": true, "title": true, "template": true,
}

// paragraphElements are set off from the text around them by a blank line.
var paragraphElements = map[string]bool{
	"p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"blockquote": true, "table": true, "pre": true, "ul": true, "ol": true,
}

// lineElements start and end on a line of their own.
var lineElements = map[string]bool{
	"div": true, "li": true, "tr": true, "dd": true, "dt": true, "hr": true,
	"section": true, "article": true, "header": true, "footer": true, "address": true,
}

// HTMLText renders an HTML body as the plain text a reader of it sees: the text
// of the visible elements, with a line break where the markup breaks a line and
// the whitespace of the source collapsed as a browser collapses it.
func HTMLText(body string) string {
	z := html.NewTokenizer(strings.NewReader(body))
	var w textWriter
	for {
		switch z.Next() {
		case html.ErrorToken:
			return w.String()
		case html.TextToken:
			if w.hidden == 0 {
				w.text(string(z.Text()))
			}
		case html.StartTagToken:
			name, _ := z.TagName()
			w.open(string(name))
		case html.SelfClosingTagToken:
			name, _ := z.TagName()
			w.breakFor(string(name))
		case html.EndTagToken:
			name, _ := z.TagName()
			w.close(string(name))
		}
	}
}

// textWriter accumulates the text of an HTML body.
type textWriter struct {
	b      strings.Builder
	space  bool // whitespace separates the next word from the text before it
	hidden int  // open hidden elements
	pre    int  // open pre elements
}

// open enters an element.
func (w *textWriter) open(name string) {
	switch {
	case hiddenElements[name]:
		w.hidden++
	case name == "pre":
		w.pre++
	}
	w.breakFor(name)
}

// close leaves an element.
func (w *textWriter) close(name string) {
	switch {
	case hiddenElements[name]:
		if w.hidden > 0 {
			w.hidden--
		}
		return
	case name == "pre" && w.pre > 0:
		w.pre--
	}
	if name != "br" {
		w.breakFor(name)
	}
}

// breakFor writes the line break an element's boundary makes.
func (w *textWriter) breakFor(name string) {
	switch {
	case name == "br":
		w.newline()
	case paragraphElements[name]:
		w.endLine()
		if w.b.Len() > 0 && !strings.HasSuffix(w.b.String(), "\n\n") {
			w.newline()
		}
	case lineElements[name]:
		w.endLine()
	case name == "td" || name == "th":
		w.space = true
	}
}

// text writes a text run. Outside a pre element each run of whitespace becomes
// one space, and whitespace at the start of a line is dropped.
func (w *textWriter) text(s string) {
	if w.pre > 0 {
		w.b.WriteString(s)
		w.space = false
		return
	}
	if r, _ := utf8.DecodeRuneInString(s); unicode.IsSpace(r) {
		w.space = true
	}
	words := strings.Fields(s)
	for _, word := range words {
		if w.space && !w.atLineStart() {
			w.b.WriteByte(' ')
		}
		w.b.WriteString(word)
		w.space = true
	}
	if len(words) > 0 {
		r, _ := utf8.DecodeLastRuneInString(s)
		w.space = unicode.IsSpace(r)
	}
}

// newline ends the current line.
func (w *textWriter) newline() {
	w.b.WriteByte('\n')
	w.space = false
}

// endLine ends the current line unless nothing has been written on it.
func (w *textWriter) endLine() {
	if !w.atLineStart() {
		w.newline()
	}
}

// atLineStart reports whether the next text starts a line.
func (w *textWriter) atLineStart() bool {
	s := w.b.String()
	return s == "" || strings.HasSuffix(s, "\n")
}

// String returns the text without the blank lines around it.
func (w *textWriter) String() string {
	return strings.TrimSpace(w.b.String())
}

// fillPlainBody gives a message whose only body is HTML a plain text body: the
// text of that HTML. A client reading the message as plain text, and a rule or a
// search testing its body text, read PR_BODY, which such a message otherwise
// lacks.
func fillPlainBody(msg *Message) {
	if text := PlainBodyFromHTML(msg.Props); text != "" {
		msg.Props.Set(mapi.PrBody, text)
	}
}

// PlainBodyFromHTML returns the PR_BODY a message with no plain body takes from
// its PR_HTML, decoded by its PR_INTERNET_CPID, or "" when the message has a plain
// body or no HTML body.
func PlainBodyFromHTML(props mapi.PropertyValues) string {
	if _, ok := props.GetAnyCharset(mapi.PrBody); ok {
		return ""
	}
	raw, ok := bytesProp(props, mapi.PrHTML)
	if !ok {
		return ""
	}
	body := string(raw)
	if cset := htmlCharset(props); cset != "utf-8" {
		body = mime.DecodeCharset(raw, cset)
	}
	return HTMLText(body)
}

package webmail2api

import "strings"

// kqlQuery holds the parsed field filters and the general (unfielded) terms of a
// KQL-style search query. A field is "field:value" (e.g. "from:alice"); any token
// without a recognised field prefix is a general term matched against the whole
// message. Nil slices mean the filter is absent; pointers distinguish "unset"
// from the zero value (Read = false vs not filtered).
type kqlQuery struct {
	From     []string
	To       []string
	Subject  []string
	Body     []string
	Category []string
	General  []string
	HasAtt   *bool
	Read     *bool
}

// parseKQL splits a query into field filters and general terms. Field names are
// matched case-insensitively; a value may be unquoted (one token) or quoted
// ("a b"). Unknown prefixes fall back to general terms so "kindle:book" still
// searches the message. Examples:
//
//	from:alice subject:report quarterly
//	has:attachment is:unread
//	category:"VIP Friends"
func parseKQL(q string) kqlQuery {
	var out kqlQuery
	for _, tok := range tokenizeKQL(q) {
		key, val, found := strings.Cut(tok, ":")
		if !found || val == "" {
			out.General = append(out.General, strings.ToLower(tok))
			continue
		}
		if apply, known := kqlFields[strings.ToLower(key)]; known {
			apply(&out, val)
			continue
		}
		// An unknown prefix is not a field, so the whole token searches the
		// message as written.
		out.General = append(out.General, strings.ToLower(tok))
	}
	return out
}

// kqlFields is the single source of the field names a query accepts. A name
// absent from it falls through to a general term.
var kqlFields = map[string]func(*kqlQuery, string){
	"from":       func(q *kqlQuery, v string) { q.From = append(q.From, strings.ToLower(v)) },
	"sender":     func(q *kqlQuery, v string) { q.From = append(q.From, strings.ToLower(v)) },
	"to":         func(q *kqlQuery, v string) { q.To = append(q.To, strings.ToLower(v)) },
	"recipient":  func(q *kqlQuery, v string) { q.To = append(q.To, strings.ToLower(v)) },
	"subject":    func(q *kqlQuery, v string) { q.Subject = append(q.Subject, strings.ToLower(v)) },
	"body":       func(q *kqlQuery, v string) { q.Body = append(q.Body, strings.ToLower(v)) },
	"category":   func(q *kqlQuery, v string) { q.Category = append(q.Category, strings.ToLower(v)) },
	"categories": func(q *kqlQuery, v string) { q.Category = append(q.Category, strings.ToLower(v)) },
	"has":        applyHasFilter,
	"is":         applyIsFilter,
}

// applyHasFilter sets the attachment filter. "has:" with any other value names
// no filter this server implements, so it is ignored rather than guessed at.
func applyHasFilter(q *kqlQuery, v string) {
	if !strings.EqualFold(v, "attachment") && !strings.EqualFold(v, "attachments") {
		return
	}
	b := kqlBool(v)
	q.HasAtt = &b
}

// applyIsFilter sets the read filter from is:read / is:unread.
func applyIsFilter(q *kqlQuery, v string) {
	b := kqlBool(v)
	switch {
	case strings.EqualFold(v, "read"):
		q.Read = &b
	case strings.EqualFold(v, "unread"):
		f := !b
		q.Read = &f
	}
}

// kqlBool maps yes/true/1/attachment (and bare field names) to true.
func kqlBool(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "1", "attachment", "attachments", "read", "unread":
		return true
	}
	return false
}

// tokenizeKQL splits on whitespace, honoring double-quoted phrases. Quotes are
// stripped from the returned tokens.
func tokenizeKQL(q string) []string {
	var tokens []string
	var b strings.Builder
	inQuote := false
	for _, r := range q {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if b.Len() > 0 {
				tokens = append(tokens, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		tokens = append(tokens, b.String())
	}
	return tokens
}

// containsAny reports whether haystack contains any of the needles.
func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return len(needles) == 0
}

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
	tokens := tokenizeKQL(q)
	for _, tok := range tokens {
		key, val, found := strings.Cut(tok, ":")
		if !found || val == "" {
			out.General = append(out.General, strings.ToLower(tok))
			continue
		}
		switch strings.ToLower(key) {
		case "from", "sender":
			out.From = append(out.From, strings.ToLower(val))
		case "to", "recipient":
			out.To = append(out.To, strings.ToLower(val))
		case "subject":
			out.Subject = append(out.Subject, strings.ToLower(val))
		case "body":
			out.Body = append(out.Body, strings.ToLower(val))
		case "category", "categories":
			out.Category = append(out.Category, strings.ToLower(val))
		case "has":
			b := kqlBool(val)
			if strings.EqualFold(val, "attachment") || strings.EqualFold(val, "attachments") {
				out.HasAtt = &b
			}
		case "is":
			b := kqlBool(val)
			if strings.EqualFold(val, "read") {
				out.Read = &b
			} else if strings.EqualFold(val, "unread") {
				f := !b
				out.Read = &f
			}
		default:
			out.General = append(out.General, strings.ToLower(tok))
		}
	}
	return out
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

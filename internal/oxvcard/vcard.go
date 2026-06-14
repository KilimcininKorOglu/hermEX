package oxvcard

import (
	"bytes"
	"strings"
)

// vline is one parsed content line: a property name, its parameters (each a
// list, e.g. TYPE=work,voice), and the raw (still-escaped) value text.
type vline struct {
	name   string
	params map[string][]string
	value  string
}

// vcard is one parsed vCard: its content lines in document order.
type vcard struct {
	lines []vline
}

// parseVCard parses the first BEGIN:VCARD…END:VCARD block in raw. Lines are
// unfolded first (RFC 6350 §3.2). It returns an error if no card is present.
func parseVCard(raw []byte) (*vcard, error) {
	logical := unfold(raw)
	c := &vcard{}
	in := false
	for _, line := range logical {
		if line == "" {
			continue
		}
		name, params, value := splitLine(line)
		upper := strings.ToUpper(name)
		switch {
		case upper == "BEGIN" && strings.EqualFold(value, "VCARD"):
			in = true
			continue
		case upper == "END" && strings.EqualFold(value, "VCARD"):
			if in {
				return c, nil
			}
			continue
		case !in:
			continue
		}
		c.lines = append(c.lines, vline{name: upper, params: params, value: value})
	}
	if !in || len(c.lines) == 0 {
		return nil, errNoCard
	}
	return c, nil
}

// unfold splits raw into logical lines, joining RFC 6350 continuation lines (a
// physical line beginning with a space or tab continues the previous one). It
// tolerates both CRLF and LF.
func unfold(raw []byte) []string {
	physical := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var out []string
	for _, p := range physical {
		if (strings.HasPrefix(p, " ") || strings.HasPrefix(p, "\t")) && len(out) > 0 {
			out[len(out)-1] += p[1:]
			continue
		}
		out = append(out, p)
	}
	return out
}

// splitLine splits a logical content line into its property name, parameters,
// and raw value. A group prefix ("item1.EMAIL") is dropped. The value is
// everything after the first unquoted colon, left escaped.
func splitLine(line string) (name string, params map[string][]string, value string) {
	colon := indexNameColon(line)
	left, value := line, ""
	if colon >= 0 {
		left, value = line[:colon], line[colon+1:]
	}
	parts := strings.Split(left, ";")
	name = parts[0]
	if dot := strings.IndexByte(name, '.'); dot >= 0 { // strip group prefix
		name = name[dot+1:]
	}
	params = map[string][]string{}
	for _, p := range parts[1:] {
		key, val, ok := strings.Cut(p, "=")
		if !ok { // bare parameter is an implicit TYPE
			key, val = "TYPE", p
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		for _, v := range strings.Split(val, ",") {
			params[key] = append(params[key], strings.Trim(strings.TrimSpace(v), `"`))
		}
	}
	return name, params, value
}

// indexNameColon returns the index of the colon separating the name/params from
// the value, skipping any colon inside a double-quoted parameter value.
func indexNameColon(line string) int {
	quoted := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			quoted = !quoted
		case ':':
			if !quoted {
				return i
			}
		}
	}
	return -1
}

// types returns the line's TYPE parameter values, lowercased.
func (l vline) types() []string {
	var out []string
	for _, t := range l.params["TYPE"] {
		out = append(out, strings.ToLower(t))
	}
	return out
}

// hasType reports whether the line carries the given TYPE value (case-insensitive).
func (l vline) hasType(t string) bool {
	for _, v := range l.types() {
		if v == t {
			return true
		}
	}
	return false
}

// text returns the line's value as a single unescaped string.
func (l vline) text() string {
	return unescapeValue(l.value)
}

// components splits a structured value (e.g. N, ADR) into its ";"-separated
// components, each unescaped. Multi-valued components keep their ","-separated
// pieces joined by ", " (sufficient for the contact fields mapped here).
func (l vline) components() []string {
	parts := splitEscaped(l.value, ';')
	out := make([]string, len(parts))
	for i, p := range parts {
		sub := splitEscaped(p, ',')
		for j := range sub {
			sub[j] = unescapeValue(sub[j])
		}
		out[i] = strings.Join(sub, ", ")
	}
	return out
}

// component returns the i-th structured component, or "" when absent.
func (l vline) component(i int) string {
	c := l.components()
	if i < len(c) {
		return c[i]
	}
	return ""
}

// get returns the first line with the given (uppercase) name, or nil.
func (c *vcard) get(name string) *vline {
	for i := range c.lines {
		if c.lines[i].name == name {
			return &c.lines[i]
		}
	}
	return nil
}

// all returns every line with the given (uppercase) name, in document order.
func (c *vcard) all(name string) []vline {
	var out []vline
	for _, l := range c.lines {
		if l.name == name {
			out = append(out, l)
		}
	}
	return out
}

// version returns the VERSION value, or "".
func (c *vcard) version() string {
	if l := c.get("VERSION"); l != nil {
		return strings.TrimSpace(l.text())
	}
	return ""
}

// splitEscaped splits s on the given separator, honoring backslash escapes so an
// escaped separator (e.g. "\,") does not split.
func splitEscaped(s string, sep byte) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			cur.WriteByte(s[i])
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if s[i] == sep {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	out = append(out, cur.String())
	return out
}

// unescapeValue reverses RFC 6350 value escaping: \\ \, \; and \n/\N (newline).
func unescapeValue(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				b.WriteByte('\n')
			case '\\', ',', ';':
				b.WriteByte(s[i+1])
			default:
				b.WriteByte(s[i+1])
			}
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// escapeValue applies RFC 6350 value escaping to a single component value.
func escapeValue(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', ',', ';':
			b.WriteByte('\\')
			b.WriteByte(s[i])
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			// drop bare CR; the \n handling carries the line break
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// builder accumulates content lines for serialization.
type builder struct {
	buf bytes.Buffer
}

// add writes one content line "NAME[;params]:value", value already escaped, then
// folds it to 75 octets per RFC 6350 §3.2.
func (b *builder) add(line string) {
	fold(&b.buf, line)
}

// line writes a simple "NAME:value" line with the value escaped.
func (b *builder) line(name, value string) {
	b.add(name + ":" + escapeValue(value))
}

// fold writes s to buf as one logical line, breaking physical lines at 75 octets
// with a leading space on each continuation, terminated by CRLF.
func fold(buf *bytes.Buffer, s string) {
	const limit = 75
	for len(s) > limit {
		buf.WriteString(s[:limit])
		buf.WriteString("\r\n ")
		s = s[limit:]
	}
	buf.WriteString(s)
	buf.WriteString("\r\n")
}

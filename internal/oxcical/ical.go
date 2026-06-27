package oxcical

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"time"
)

var errNoCalendar = errors.New("oxcical: no BEGIN:VCALENDAR block")

// iline is one parsed content line: a property name, its parameters (each a list,
// e.g. TZID=…;VALUE=DATE), and the raw (still-escaped) value text.
type iline struct {
	name   string
	params map[string][]string
	value  string
}

// icomp is one parsed iCalendar component (VCALENDAR, VEVENT, VTIMEZONE, VALARM,
// …): its own content lines plus nested sub-components, in document order.
type icomp struct {
	name  string
	props []iline
	comps []*icomp
}

// parseICal parses raw into the top-level component (the VCALENDAR). Lines are
// unfolded first (RFC 5545 §3.1); BEGIN/END pairs nest sub-components.
func parseICal(raw []byte) (*icomp, error) {
	var stack []*icomp
	var root *icomp
	for _, line := range unfold(raw) {
		if line == "" {
			continue
		}
		name, params, value := splitLine(line)
		switch strings.ToUpper(name) {
		case "BEGIN":
			c := &icomp{name: strings.ToUpper(strings.TrimSpace(value))}
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.comps = append(top.comps, c)
			} else if root == nil {
				root = c
			}
			stack = append(stack, c)
		case "END":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.props = append(top.props, iline{name: strings.ToUpper(name), params: params, value: value})
			}
		}
	}
	if root == nil {
		return nil, errNoCalendar
	}
	return root, nil
}

// unfold splits raw into logical lines, joining RFC 5545 continuation lines (a
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

// splitLine splits a logical content line into its property name, parameters, and
// raw value. The value is everything after the first unquoted colon, left escaped.
func splitLine(line string) (name string, params map[string][]string, value string) {
	colon := indexNameColon(line)
	left := line
	if colon >= 0 {
		left, value = line[:colon], line[colon+1:]
	}
	parts := strings.Split(left, ";")
	name = strings.TrimSpace(parts[0])
	params = map[string][]string{}
	for _, p := range parts[1:] {
		key, val, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		for v := range strings.SplitSeq(val, ",") {
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

// param returns the line's first value for the named parameter, or "".
func (l iline) param(key string) string {
	if v := l.params[strings.ToUpper(key)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// text returns the line's value as a single unescaped string.
func (l iline) text() string { return unescapeValue(l.value) }

// prop returns the first content line with the given name (case-insensitive), or nil.
func (c *icomp) prop(name string) *iline {
	up := strings.ToUpper(name)
	for i := range c.props {
		if c.props[i].name == up {
			return &c.props[i]
		}
	}
	return nil
}

// propText returns the unescaped value of the named content line, or "".
func (c *icomp) propText(name string) string {
	if l := c.prop(name); l != nil {
		return l.text()
	}
	return ""
}

// propLines returns every content line with the given name (case-insensitive), in
// document order, used for repeatable properties such as ATTENDEE.
func (c *icomp) propLines(name string) []iline {
	up := strings.ToUpper(name)
	var out []iline
	for i := range c.props {
		if c.props[i].name == up {
			out = append(out, c.props[i])
		}
	}
	return out
}

// sub returns the first nested component with the given name, or nil.
func (c *icomp) sub(name string) *icomp {
	up := strings.ToUpper(name)
	for _, s := range c.comps {
		if s.name == up {
			return s
		}
	}
	return nil
}

// parseICalTime parses a DATE or DATE-TIME property to a UTC instant. allDay is
// true for a date-only (VALUE=DATE) value. Resolution: a trailing Z is UTC; a TZID
// names an IANA zone resolved via time.LoadLocation; otherwise the value is
// floating and read as UTC (a documented v1 simplification). ok is false on any
// parse failure.
func parseICalTime(l *iline) (t time.Time, allDay bool, ok bool) {
	v := strings.TrimSpace(l.value)
	if strings.EqualFold(l.param("VALUE"), "DATE") || (len(v) == 8 && !strings.Contains(v, "T")) {
		d, err := time.Parse("20060102", v)
		if err != nil {
			return time.Time{}, false, false
		}
		return d.UTC(), true, true
	}
	if strings.HasSuffix(v, "Z") {
		dt, err := time.Parse("20060102T150405Z", v)
		if err != nil {
			return time.Time{}, false, false
		}
		return dt.UTC(), false, true
	}
	if tzid := l.param("TZID"); tzid != "" {
		if loc, err := time.LoadLocation(tzid); err == nil {
			if dt, err := time.ParseInLocation("20060102T150405", v, loc); err == nil {
				return dt.UTC(), false, true
			}
		}
	}
	dt, err := time.Parse("20060102T150405", v)
	if err != nil {
		return time.Time{}, false, false
	}
	return dt.UTC(), false, true
}

// formatICalUTC renders a UTC instant as an iCalendar DATE-TIME with a Z suffix.
func formatICalUTC(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// formatICalDate renders an instant as an iCalendar DATE value (date only).
func formatICalDate(t time.Time) string { return t.UTC().Format("20060102") }

// parseICalDuration parses an RFC 5545 DURATION (e.g. "PT15M", "-PT1H30M", "P1D",
// "P1W") into a signed Go duration. Month/year designators are not supported.
func parseICalDuration(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	if !strings.HasPrefix(s, "P") {
		return 0, false
	}
	s = s[1:]
	var d time.Duration
	inTime := false
	num := ""
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == 'T' {
			inTime = true
			continue
		}
		if ch >= '0' && ch <= '9' {
			num += string(ch)
			continue
		}
		n, _ := strconv.Atoi(num)
		num = ""
		switch ch {
		case 'W':
			d += time.Duration(n) * 7 * 24 * time.Hour
		case 'D':
			d += time.Duration(n) * 24 * time.Hour
		case 'H':
			d += time.Duration(n) * time.Hour
		case 'M':
			if inTime {
				d += time.Duration(n) * time.Minute
			}
		case 'S':
			d += time.Duration(n) * time.Second
		}
	}
	if neg {
		d = -d
	}
	return d, true
}

// unescapeValue reverses RFC 5545 TEXT escaping: \\ \, \; and \n/\N (newline).
func unescapeValue(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				b.WriteByte('\n')
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

// escapeValue applies RFC 5545 TEXT escaping to a value.
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

// add writes one content line verbatim (value already escaped where needed),
// folded to 75 octets per RFC 5545 §3.1.
func (b *builder) add(line string) { fold(&b.buf, line) }

// line writes a simple "NAME:value" line with the value TEXT-escaped.
func (b *builder) line(name, value string) { b.add(name + ":" + escapeValue(value)) }

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

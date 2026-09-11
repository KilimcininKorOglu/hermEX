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

// maxNestingDepth bounds how deeply parseICal nests sub-components. The deepest
// legal iCalendar structure is three levels (VCALENDAR > VEVENT > VALARM), so
// the bound is far above anything a real calendar carries. It exists because the
// resulting tree is walked by recursive descent (writeComponent, filterComp) and
// is built from bytes an inbound sender or CalDAV client chose: without it a run
// of BEGIN lines costs about 14 bytes per stack frame and kills the process with
// an unrecoverable stack overflow. A component past the bound, and everything it
// contains, is dropped rather than failing the whole parse, so a malformed part
// never rejects the message carrying it.
const maxNestingDepth = 20

// parseICal parses raw into the top-level component (the VCALENDAR). Lines are
// unfolded first (RFC 5545 §3.1); BEGIN/END pairs nest sub-components up to
// maxNestingDepth.
func parseICal(raw []byte) (*icomp, error) {
	var p icalParser
	for _, line := range unfold(raw) {
		if line == "" {
			continue
		}
		name, params, value := splitLine(line)
		switch strings.ToUpper(name) {
		case "BEGIN":
			p.begin(value)
		case "END":
			p.end()
		default:
			p.property(iline{name: strings.ToUpper(name), params: params, value: value})
		}
	}
	if p.root == nil {
		return nil, errNoCalendar
	}
	return p.root, nil
}

// icalParser builds the component tree as the logical lines are read.
type icalParser struct {
	stack []*icomp
	root  *icomp
	// depth counts every open BEGIN, including those past the bound, so that a
	// matching END pops the same level it opened.
	depth int
}

// top is the component currently being filled, or nil outside any component.
func (p *icalParser) top() *icomp {
	if len(p.stack) == 0 {
		return nil
	}
	return p.stack[len(p.stack)-1]
}

// begin opens a component. One past the nesting bound is counted but not built, so
// everything it would contain is dropped.
func (p *icalParser) begin(value string) {
	p.depth++
	if p.depth > maxNestingDepth {
		return
	}
	c := &icomp{name: strings.ToUpper(strings.TrimSpace(value))}
	if top := p.top(); top != nil {
		top.comps = append(top.comps, c)
	} else if p.root == nil {
		p.root = c
	}
	p.stack = append(p.stack, c)
}

// end closes the component the matching BEGIN opened.
func (p *icalParser) end() {
	if p.depth <= maxNestingDepth && len(p.stack) > 0 {
		p.stack = p.stack[:len(p.stack)-1]
	}
	if p.depth > 0 {
		p.depth--
	}
}

// property files a content line on the component being filled.
func (p *icalParser) property(l iline) {
	if p.depth > maxNestingDepth {
		return
	}
	if top := p.top(); top != nil {
		top.props = append(top.props, l)
	}
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
//
// A nil line is one such failure, not a programming error: callers pass the result
// of prop() straight in, and a component that does not carry the property at all
// is ordinary inbound data (a VEVENT with RRULE but no DTSTART). Reading it as
// "unparseable" lets every caller take the branch it already has for a bad value.
func parseICalTime(l *iline) (t time.Time, allDay bool, ok bool) {
	if l == nil {
		return time.Time{}, false, false
	}
	v := strings.TrimSpace(l.value)
	if isDateOnly(l, v) {
		d, err := time.Parse("20060102", v)
		if err != nil {
			return time.Time{}, false, false
		}
		return d.UTC(), true, true
	}
	dt, ok := parseICalDateTime(l, v)
	if !ok {
		return time.Time{}, false, false
	}
	return dt, false, true
}

// isDateOnly reports whether a property value is a date rather than a date-time,
// either declared with VALUE=DATE or recognizable by its length.
func isDateOnly(l *iline, v string) bool {
	return strings.EqualFold(l.param("VALUE"), "DATE") || (len(v) == 8 && !strings.Contains(v, "T"))
}

// parseICalDateTime resolves a date-time value to a UTC instant: a trailing Z is
// UTC, then the property's TZID, then a floating value read as UTC.
func parseICalDateTime(l *iline, v string) (time.Time, bool) {
	if strings.HasSuffix(v, "Z") {
		dt, err := time.Parse("20060102T150405Z", v)
		if err != nil {
			return time.Time{}, false
		}
		return dt.UTC(), true
	}
	if tzid := l.param("TZID"); tzid != "" {
		if loc, err := time.LoadLocation(tzid); err == nil {
			if dt, err := time.ParseInLocation("20060102T150405", v, loc); err == nil {
				return dt.UTC(), true
			}
		}
	}
	dt, err := time.Parse("20060102T150405", v)
	if err != nil {
		return time.Time{}, false
	}
	return dt.UTC(), true
}

// formatICalUTC renders a UTC instant as an iCalendar DATE-TIME with a Z suffix.
func formatICalUTC(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// formatICalDate renders an instant as an iCalendar DATE value (date only).
func formatICalDate(t time.Time) string { return t.UTC().Format("20060102") }

// parseICalDuration parses an RFC 5545 DURATION (e.g. "PT15M", "-PT1H30M", "P1D",
// "P1W") into a signed Go duration. Month/year designators are not supported.
func parseICalDuration(s string) (time.Duration, bool) {
	body, neg, ok := splitDurationSign(strings.TrimSpace(s))
	if !ok {
		return 0, false
	}
	d := sumDurationUnits(body)
	if neg {
		return -d, true
	}
	return d, true
}

// splitDurationSign strips the optional sign and the mandatory "P" designator,
// returning the unit sequence that follows.
func splitDurationSign(s string) (body string, neg, ok bool) {
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	if !strings.HasPrefix(s, "P") {
		return "", false, false
	}
	return s[1:], neg, true
}

// sumDurationUnits adds up the "<number><unit>" pairs of a duration body. The "T"
// designator switches to the time part, where "M" means minutes rather than months.
func sumDurationUnits(s string) time.Duration {
	var d time.Duration
	inTime := false
	num := ""
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == 'T':
			inTime = true
		case ch >= '0' && ch <= '9':
			num += string(ch)
		default:
			n, _ := strconv.Atoi(num)
			d += durationUnit(ch, n, inTime)
			num = ""
		}
	}
	return d
}

// durationUnit scales a count by its unit designator. Month and year designators
// are not supported, and an "M" outside the time part contributes nothing.
func durationUnit(unit byte, n int, inTime bool) time.Duration {
	switch unit {
	case 'W':
		return time.Duration(n) * 7 * 24 * time.Hour
	case 'D':
		return time.Duration(n) * 24 * time.Hour
	case 'H':
		return time.Duration(n) * time.Hour
	case 'M':
		if inTime {
			return time.Duration(n) * time.Minute
		}
	case 'S':
		return time.Duration(n) * time.Second
	}
	return 0
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

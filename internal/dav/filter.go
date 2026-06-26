package dav

import (
	"strings"
	"time"
)

// CalDAV/CardDAV query filters (RFC 4791 §9.7, RFC 6352 §10.5). The filter tree is
// parsed from the REPORT body and evaluated against each resource's iCalendar or
// vCard text; a resource is returned only when the filter matches. The matcher
// parses just enough of the resource (component tree, properties, parameters,
// DTSTART/DTEND for time-range) to evaluate the filter — it is not a full RFC 5545
// model.

// filter is the <C:filter> of a calendar-query (one VCALENDAR comp-filter) or the
// <C:filter> of an addressbook-query (prop-filters with a test mode).
type filter struct {
	Test        string       `xml:"test,attr"` // CardDAV: anyof (default) | allof
	CompFilters []compFilter `xml:"comp-filter"`
	PropFilters []propFilter `xml:"prop-filter"`
}

type compFilter struct {
	Name         string       `xml:"name,attr"`
	IsNotDefined *struct{}    `xml:"is-not-defined"`
	TimeRange    *timeRange   `xml:"time-range"`
	PropFilters  []propFilter `xml:"prop-filter"`
	CompFilters  []compFilter `xml:"comp-filter"`
}

type propFilter struct {
	Name         string        `xml:"name,attr"`
	Test         string        `xml:"test,attr"` // CardDAV: anyof (default) | allof
	IsNotDefined *struct{}     `xml:"is-not-defined"`
	TimeRange    *timeRange    `xml:"time-range"`
	TextMatch    *textMatch    `xml:"text-match"`
	ParamFilters []paramFilter `xml:"param-filter"`
}

type paramFilter struct {
	Name         string     `xml:"name,attr"`
	IsNotDefined *struct{}  `xml:"is-not-defined"`
	TextMatch    *textMatch `xml:"text-match"`
}

type timeRange struct {
	Start string `xml:"start,attr"`
	End   string `xml:"end,attr"`
}

type textMatch struct {
	Collation       string `xml:"collation,attr"`
	NegateCondition string `xml:"negate-condition,attr"`
	MatchType       string `xml:"match-type,attr"` // CardDAV: contains|equals|starts-with|ends-with
	Value           string `xml:",chardata"`
}

// --- minimal iCalendar/vCard component model for matching ---

type icalNode struct {
	name  string
	props []icalProp
	kids  []*icalNode
}

type icalProp struct {
	name   string
	params map[string][]string
	value  string
}

// parseICalNode parses iCalendar/vCard text into a component tree. Both formats
// share the BEGIN/END + folded "NAME;PARAM=v:value" line grammar.
func parseICalNode(raw string) *icalNode {
	var stack []*icalNode
	var root *icalNode
	for _, line := range unfoldLines(raw) {
		if line == "" {
			continue
		}
		name, params, value := splitContentLine(line)
		switch strings.ToUpper(name) {
		case "BEGIN":
			n := &icalNode{name: strings.ToUpper(strings.TrimSpace(value))}
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.kids = append(top.kids, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case "END":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.props = append(top.props, icalProp{name: strings.ToUpper(name), params: params, value: value})
			}
		}
	}
	return root
}

// unfoldLines splits raw into logical lines, joining RFC 5545/6350 continuation
// lines (a physical line starting with space/tab continues the previous one).
func unfoldLines(raw string) []string {
	physical := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
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

// splitContentLine splits "NAME;PARAM=v:value" into the name, its parameters, and
// the value (everything after the first unquoted colon).
func splitContentLine(line string) (name string, params map[string][]string, value string) {
	colon := -1
	quoted := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			quoted = !quoted
		case ':':
			if !quoted {
				colon = i
			}
		}
		if colon >= 0 {
			break
		}
	}
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

func (n *icalNode) propsByName(name string) []icalProp {
	up := strings.ToUpper(name)
	var out []icalProp
	for _, p := range n.props {
		if p.name == up {
			out = append(out, p)
		}
	}
	return out
}

func (p icalProp) param(key string) string {
	if v := p.params[strings.ToUpper(key)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// --- calendar-query evaluation (RFC 4791 §9.7) ---

// calendarMatches reports whether an iCalendar resource satisfies a calendar-query
// filter. A nil/empty filter matches everything.
func calendarMatches(f *filter, ical string) bool {
	if f == nil || len(f.CompFilters) == 0 {
		return true
	}
	root := parseICalNode(ical)
	if root == nil {
		return false
	}
	return matchComp(f.CompFilters[0], []*icalNode{root})
}

// matchComp evaluates a comp-filter against the candidate components at one level:
// is-not-defined wants none of that name, otherwise at least one must satisfy the
// nested time-range, prop-filters, and comp-filters.
func matchComp(cf compFilter, comps []*icalNode) bool {
	var named []*icalNode
	for _, c := range comps {
		if strings.EqualFold(c.name, cf.Name) {
			named = append(named, c)
		}
	}
	if cf.IsNotDefined != nil {
		return len(named) == 0
	}
	for _, c := range named {
		if compSatisfies(cf, c) {
			return true
		}
	}
	return false
}

func compSatisfies(cf compFilter, c *icalNode) bool {
	if cf.TimeRange != nil && !timeRangeMatchesComp(cf.TimeRange, c) {
		return false
	}
	for _, pf := range cf.PropFilters {
		if !matchPropCal(pf, c) {
			return false
		}
	}
	for _, sub := range cf.CompFilters {
		if !matchComp(sub, c.kids) {
			return false
		}
	}
	return true
}

// matchPropCal evaluates a prop-filter against a component's properties.
func matchPropCal(pf propFilter, c *icalNode) bool {
	props := c.propsByName(pf.Name)
	if pf.IsNotDefined != nil {
		return len(props) == 0
	}
	for _, p := range props {
		if pf.TextMatch != nil && !textMatches(pf.TextMatch, unescapeText(p.value)) {
			continue
		}
		ok := true
		for _, paf := range pf.ParamFilters {
			if !matchParam(paf, p) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func matchParam(paf paramFilter, p icalProp) bool {
	vals := p.params[strings.ToUpper(paf.Name)]
	if paf.IsNotDefined != nil {
		return len(vals) == 0
	}
	if len(vals) == 0 {
		return false
	}
	if paf.TextMatch == nil {
		return true
	}
	for _, v := range vals {
		if textMatches(paf.TextMatch, v) {
			return true
		}
	}
	return false
}

// textMatches applies an RFC 4791/6352 text-match: a substring test by default
// (CardDAV match-type may request equals/starts-with/ends-with), caseless unless an
// octet collation is named, with negate-condition flipping the result.
func textMatches(tm *textMatch, value string) bool {
	got, want := value, tm.Value
	if !strings.Contains(strings.ToLower(tm.Collation), "octet") {
		got, want = strings.ToLower(got), strings.ToLower(want)
	}
	var matched bool
	switch strings.ToLower(tm.MatchType) {
	case "equals":
		matched = got == want
	case "starts-with":
		matched = strings.HasPrefix(got, want)
	case "ends-with":
		matched = strings.HasSuffix(got, want)
	default: // "contains" and the CalDAV default
		matched = strings.Contains(got, want)
	}
	if strings.EqualFold(tm.NegateCondition, "yes") {
		return !matched
	}
	return matched
}

// --- time-range (RFC 4791 §9.9) ---

// timeRangeMatchesComp reports whether a component's effective [start,end) overlaps
// the filter range. The component's span is DTSTART plus DTEND or DURATION (an
// all-day DTSTART with neither spans one day; a timed DTSTART with neither is a
// zero-length instant).
func timeRangeMatchesComp(tr *timeRange, c *icalNode) bool {
	rangeStart, okS := parseFilterTime(tr.Start)
	rangeEnd, okE := parseFilterTime(tr.End)
	dtstart := c.propsByName("DTSTART")
	if len(dtstart) == 0 {
		return false
	}
	start, allDay, ok := propTime(dtstart[0])
	if !ok {
		return false
	}
	end := start
	if dt := c.propsByName("DTEND"); len(dt) > 0 {
		if e, _, ok := propTime(dt[0]); ok {
			end = e
		}
	} else if du := c.propsByName("DURATION"); len(du) > 0 {
		if d, ok := parseDuration(du[0].value); ok {
			end = start.Add(d)
		}
	} else if allDay {
		end = start.Add(24 * time.Hour)
	}
	// Overlap: start < rangeEnd AND end > rangeStart, with an open-ended range when
	// a bound was omitted or unparseable.
	if okS && !end.After(rangeStart) {
		return false
	}
	if okE && !start.Before(rangeEnd) {
		return false
	}
	return true
}

// parseFilterTime parses a time-range bound: an iCalendar UTC DATE-TIME (RFC 4791
// requires the Z form).
func parseFilterTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse("20060102T150405Z", v); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse("20060102", v); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// propTime parses a DTSTART/DTEND value to a UTC instant; allDay marks a VALUE=DATE.
func propTime(p icalProp) (t time.Time, allDay bool, ok bool) {
	v := strings.TrimSpace(p.value)
	if strings.EqualFold(p.param("VALUE"), "DATE") || (len(v) == 8 && !strings.Contains(v, "T")) {
		if d, err := time.Parse("20060102", v); err == nil {
			return d.UTC(), true, true
		}
		return time.Time{}, false, false
	}
	if strings.HasSuffix(v, "Z") {
		if dt, err := time.Parse("20060102T150405Z", v); err == nil {
			return dt.UTC(), false, true
		}
	}
	if tzid := p.param("TZID"); tzid != "" {
		if loc, err := time.LoadLocation(tzid); err == nil {
			if dt, err := time.ParseInLocation("20060102T150405", v, loc); err == nil {
				return dt.UTC(), false, true
			}
		}
	}
	if dt, err := time.Parse("20060102T150405", v); err == nil {
		return dt.UTC(), false, true
	}
	return time.Time{}, false, false
}

// parseDuration parses an RFC 5545 DURATION (weeks/days/hours/minutes/seconds).
func parseDuration(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	} else if strings.HasPrefix(s, "+") {
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
		n := 0
		for _, c := range num {
			n = n*10 + int(c-'0')
		}
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

// unescapeText reverses RFC 5545/6350 TEXT escaping for matching.
func unescapeText(s string) string {
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

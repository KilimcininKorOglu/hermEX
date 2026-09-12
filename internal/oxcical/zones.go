package oxcical

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// ZoneNote describes one imported date-time that could not be bound to a time
// zone and was therefore read as UTC. TZID is empty when the value carried no
// TZID at all (a floating time, which RFC 5545 defines as the reader's local
// wall clock and which this package reads as UTC), and is the zone id that
// nothing could resolve otherwise.
type ZoneNote struct {
	Property string
	TZID     string
	Value    string
}

// FloatingZoneID stands, in a ZoneLosses summary, for a time that carried no zone
// id at all. It keeps the two cases apart for whoever reads the log: a sender
// whose zone id is not understood (the table can be extended) and a sender who
// wrote no zone at all (nothing to extend).
const FloatingZoneID = "(floating)"

// ZoneLosses summarizes the times one imported calendar was read as UTC for want
// of a usable zone. Its Add method is an OnUnresolvedZone callback. Every caller
// that logs this wants the same shape (which zone ids, how many times), so the
// grouping lives here rather than once per protocol package. The zero value is
// ready to use.
type ZoneLosses struct {
	times int
	ids   map[string]bool
}

// Add records one reported time.
func (z *ZoneLosses) Add(n ZoneNote) {
	if z.ids == nil {
		z.ids = map[string]bool{}
	}
	id := n.TZID
	if id == "" {
		id = FloatingZoneID
	}
	z.ids[id] = true
	z.times++
}

// Times is how many date-times were read as UTC without a zone, and is zero when
// the import lost nothing.
func (z *ZoneLosses) Times() int { return z.times }

// ZoneIDs lists the distinct zone ids involved, in a stable order.
func (z *ZoneLosses) ZoneIDs() []string {
	out := make([]string, 0, len(z.ids))
	for id := range z.ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// bindZones resolves every TZID in a parsed calendar once and records the result
// on the content line that carries it, so parseICalDateTime never has to resolve
// a zone itself. A line whose TZID nothing resolved keeps a nil location and is
// read as UTC, which is what reportZones tells the caller about.
func bindZones(root *icomp) {
	stream := collectStreamZones(root)
	bindComponent(root, stream, map[string]*time.Location{})
}

// bindComponent binds the zone of every content line in one component and its
// sub-components. A VTIMEZONE is skipped whole: the DTSTART inside its STANDARD
// and DAYLIGHT rules is floating by definition and describes the zone rather than
// an instant in it.
func bindComponent(c *icomp, stream, cache map[string]*time.Location) {
	if c.name == "VTIMEZONE" {
		return
	}
	for i := range c.props {
		tzid := c.props[i].param("TZID")
		if tzid == "" {
			continue
		}
		loc, hit := cache[tzid]
		if !hit {
			loc = resolveTZID(tzid, stream)
			cache[tzid] = loc
		}
		c.props[i].loc = loc
	}
	for _, sub := range c.comps {
		bindComponent(sub, stream, cache)
	}
}

// resolveTZID turns a TZID into a location: an IANA name first, then the Windows
// id Outlook and Exchange write, then a fixed-offset zone taken from the stream's
// own VTIMEZONE. It returns nil when none of the three answers, because reading
// the value as UTC and saying so beats storing a guessed instant.
func resolveTZID(tzid string, stream map[string]*time.Location) *time.Location {
	if loc, err := time.LoadLocation(tzid); err == nil {
		return loc
	}
	if iana, ok := ianaForWindows(tzid); ok {
		if loc, err := time.LoadLocation(iana); err == nil {
			return loc
		}
	}
	return stream[tzid]
}

// collectStreamZones reads the calendar's own VTIMEZONE components into fixed
// zones, which is the last resort for a TZID no table knows.
func collectStreamZones(root *icomp) map[string]*time.Location {
	out := map[string]*time.Location{}
	for _, c := range root.comps {
		if c.name != "VTIMEZONE" {
			continue
		}
		tzid := strings.TrimSpace(c.propText("TZID"))
		if tzid == "" {
			continue
		}
		if loc := streamZone(tzid, c); loc != nil {
			out[tzid] = loc
		}
	}
	return out
}

// streamZone builds a fixed zone from a VTIMEZONE, but only when every rule in it
// names the same UTC offset. A VTIMEZONE whose STANDARD and DAYLIGHT rules differ
// describes a transition this package does not evaluate, and one fixed offset
// would be wrong for half the year, so such a zone stays unresolved and its times
// are reported. The named tables above cover what Outlook and Exchange write.
func streamZone(tzid string, vtz *icomp) *time.Location {
	offsets := map[int]bool{}
	for _, sub := range vtz.comps {
		if sub.name != "STANDARD" && sub.name != "DAYLIGHT" {
			continue
		}
		secs, ok := parseUTCOffset(sub.propText("TZOFFSETTO"))
		if !ok {
			return nil
		}
		offsets[secs] = true
	}
	if len(offsets) != 1 {
		return nil
	}
	for secs := range offsets {
		return time.FixedZone(tzid, secs)
	}
	return nil
}

// parseUTCOffset parses an RFC 5545 UTC-OFFSET ("+0200", "-0330", "+013000") into
// seconds east of UTC.
func parseUTCOffset(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 5 && len(s) != 7 {
		return 0, false
	}
	sign, ok := offsetSign(s[0])
	if !ok {
		return 0, false
	}
	secs, ok := offsetSeconds(s[1:])
	if !ok {
		return 0, false
	}
	return sign * secs, true
}

// offsetSign reads the mandatory sign of a UTC-OFFSET.
func offsetSign(c byte) (int, bool) {
	switch c {
	case '+':
		return 1, true
	case '-':
		return -1, true
	}
	return 0, false
}

// offsetSeconds reads the HHMM or HHMMSS digits of a UTC-OFFSET as a magnitude in
// seconds.
func offsetSeconds(s string) (int, bool) {
	h, hok := twoDigits(s[0:2])
	m, mok := twoDigits(s[2:4])
	sec, sok := 0, true
	if len(s) == 6 {
		sec, sok = twoDigits(s[4:6])
	}
	if !hok || !mok || !sok || h > 23 || m > 59 || sec > 59 {
		return 0, false
	}
	return h*3600 + m*60 + sec, true
}

// twoDigits parses exactly two decimal digits, rejecting the signs and spaces
// strconv.Atoi would otherwise accept.
func twoDigits(s string) (int, bool) {
	if len(s) != 2 || s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// reportZones hands opt.OnUnresolvedZone every date-time in the calendar that is
// being read as UTC without having said so. Without it the loss is silent: the
// appointment is stored at the wrong instant and nothing in the log says which
// zone id the sender used, which is the one fact needed to extend the table.
func reportZones(root *icomp, opt Options) {
	if opt.OnUnresolvedZone == nil {
		return
	}
	var notes []ZoneNote
	collectZoneNotes(root, &notes)
	for _, n := range notes {
		opt.OnUnresolvedZone(n)
	}
}

// collectZoneNotes gathers the unbound date-times of a component and everything
// under it, in document order.
func collectZoneNotes(c *icomp, out *[]ZoneNote) {
	if c.name == "VTIMEZONE" {
		return
	}
	for i := range c.props {
		l := &c.props[i]
		if l.loc != nil || !isFloatingDateTime(l) {
			continue
		}
		*out = append(*out, ZoneNote{Property: l.name, TZID: l.param("TZID"), Value: strings.TrimSpace(l.value)})
	}
	for _, sub := range c.comps {
		collectZoneNotes(sub, out)
	}
}

// isFloatingDateTime reports whether a content line's value is a date-time with
// no zone of its own: the form YYYYMMDDTHHMMSS with no trailing Z. A date-only
// value has no zone to lose and a UTC value already carries one. For a list value
// (RDATE, EXDATE) the first entry decides, since every entry of one line shares
// the line's TZID.
func isFloatingDateTime(l *iline) bool {
	if strings.EqualFold(l.param("VALUE"), "DATE") {
		return false
	}
	v := strings.TrimSpace(l.value)
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	if len(v) != 15 || v[8] != 'T' {
		return false
	}
	for i := range len(v) {
		if i == 8 {
			continue
		}
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return true
}

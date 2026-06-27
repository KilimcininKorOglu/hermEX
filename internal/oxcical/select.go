package oxcical

import "strings"

// CompSelect is a CALDAV:comp calendar-data selection (RFC 4791 §9.6.1): the component
// to return, which of its properties (AllProp, else the named Props), and which of its
// sub-components (AllComp, else the named Comps, applied recursively).
type CompSelect struct {
	Name    string
	AllProp bool
	Props   []PropSelect
	AllComp bool
	Comps   []CompSelect
}

// PropSelect names one property to return; NoValue requests the name and parameters but
// no value (CALDAV:prop novalue="yes", RFC 4791 §9.6.4).
type PropSelect struct {
	Name    string
	NoValue bool
}

// SelectCalendarData projects an iCalendar object down to the components and properties
// named by sel (CALDAV:comp / :prop / :allcomp / :allprop). A component with neither
// AllProp nor any Props returns no properties, and one with neither AllComp nor any
// Comps returns no sub-components, exactly as the grammar's empty `prop*`/`comp*` allow
// (so the result MAY be invalid per RFC 5545 if required properties were not requested).
// ok is false when the root component name does not match, so the caller serves the
// object unprojected.
func SelectCalendarData(ical []byte, sel CompSelect) ([]byte, bool) {
	cal, err := parseICal(ical)
	if err != nil {
		return nil, false
	}
	if sel.Name != "" && !strings.EqualFold(sel.Name, cal.name) {
		return nil, false
	}
	b := &builder{}
	writeComponent(b, filterComp(cal, sel))
	return b.buf.Bytes(), true
}

// filterComp returns a copy of c holding only the properties and sub-components that sel
// selects, recursing into each kept sub-component.
func filterComp(c *icomp, sel CompSelect) *icomp {
	out := &icomp{name: c.name}
	if sel.AllProp {
		out.props = append(out.props, c.props...)
	} else {
		for _, ps := range sel.Props {
			for _, l := range c.propLines(ps.Name) {
				if ps.NoValue {
					l.value = ""
				}
				out.props = append(out.props, l)
			}
		}
	}
	if sel.AllComp {
		out.comps = append(out.comps, c.comps...)
	} else {
		for _, cs := range sel.Comps {
			up := strings.ToUpper(cs.Name)
			for _, child := range c.comps {
				if child.name == up {
					out.comps = append(out.comps, filterComp(child, cs))
				}
			}
		}
	}
	return out
}

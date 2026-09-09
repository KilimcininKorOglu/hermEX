package oxvcard

import "strings"

// PropSelect names one vCard property to return; NoValue requests the name and
// parameters but no value (CARDDAV:prop novalue="yes", RFC 6352 §10.4.2).
type PropSelect struct {
	Name    string
	NoValue bool
}

// SelectAddressData projects a vCard down to the properties named by props (or every
// property when allProp is set), per CARDDAV:address-data partial retrieval (RFC 6352
// §10.4). BEGIN:VCARD and END:VCARD are always kept; an unselected property (including
// VERSION) is dropped, so the result MAY be invalid per RFC 6350 when the client did
// not request the required properties. ok is false when no card is present.
func SelectAddressData(raw []byte, props []PropSelect, allProp bool) ([]byte, bool) {
	keep, noval := selectedProps(props)
	b := &builder{}
	in := false
	seen := false
	for _, line := range unfold(raw) {
		if line == "" {
			continue
		}
		name, _, value := splitLine(line)
		up := strings.ToUpper(name)
		switch {
		case isCardBoundary(up, "BEGIN", value):
			in, seen = true, true
			b.add(line)
		case isCardBoundary(up, "END", value):
			b.add(line)
			in = false
		case in && selected(up, keep, allProp):
			b.add(selectedLine(line, noval[up]))
		}
	}
	if !seen {
		return nil, false
	}
	return b.buf.Bytes(), true
}

// selected reports whether a property is served: every one when the client asked
// for allprop, otherwise the ones it named.
func selected(name string, keep map[string]bool, allProp bool) bool {
	return allProp || keep[name]
}

// selectedProps splits the requested properties into the set to keep and the
// subset whose value the client asked to be omitted.
func selectedProps(props []PropSelect) (keep, noval map[string]bool) {
	keep, noval = map[string]bool{}, map[string]bool{}
	for _, p := range props {
		up := strings.ToUpper(strings.TrimSpace(p.Name))
		keep[up] = true
		if p.NoValue {
			noval[up] = true
		}
	}
	return keep, noval
}

// selectedLine renders one kept content line: whole, or truncated at the colon
// when the client asked for the name without its value.
func selectedLine(line string, noValue bool) string {
	if !noValue {
		return line
	}
	i := indexNameColon(line)
	if i < 0 {
		return line
	}
	return line[:i+1]
}

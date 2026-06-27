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
	keep := map[string]bool{}
	noval := map[string]bool{}
	for _, p := range props {
		up := strings.ToUpper(strings.TrimSpace(p.Name))
		keep[up] = true
		if p.NoValue {
			noval[up] = true
		}
	}
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
		case up == "BEGIN" && strings.EqualFold(value, "VCARD"):
			in, seen = true, true
			b.add(line)
			continue
		case up == "END" && strings.EqualFold(value, "VCARD"):
			b.add(line)
			in = false
			continue
		case !in:
			continue
		}
		if !allProp && !keep[up] {
			continue
		}
		if noval[up] {
			if i := indexNameColon(line); i >= 0 {
				b.add(line[:i+1])
				continue
			}
		}
		b.add(line)
	}
	if !seen {
		return nil, false
	}
	return b.buf.Bytes(), true
}

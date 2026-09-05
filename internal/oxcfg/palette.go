package oxcfg

import (
	"encoding/xml"
	"sort"
	"strconv"
	"strings"
)

// NearestPalette maps a hex colour to the closest Outlook palette index. The
// stored format carries an INDEX, not a colour, so a colour outside the palette
// cannot round-trip and this is where it loses precision. A hex that does not
// parse yields index 0, which is Outlook's first category colour rather than a
// silently absent one.
func NearestPalette(hex string) int {
	r, g, b, ok := parseHex(hex)
	if !ok {
		return 0
	}
	best, bestDist := 0, 1<<30
	for i, p := range PaletteHex {
		pr, pg, pb, ok := parseHex(p)
		if !ok {
			continue
		}
		// Plain squared distance in RGB. The palette entries are far apart, so a
		// perceptual metric would pick the same entry and cost more to explain.
		d := sq(r-pr) + sq(g-pg) + sq(b-pb)
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// HexForPalette returns the hex a palette index renders as. An index outside the
// palette reads as the first entry, so a list written by a newer client with a
// colour hermEX does not know still displays.
func HexForPalette(idx int) string {
	if idx < 0 || idx >= len(PaletteHex) {
		return PaletteHex[0]
	}
	return PaletteHex[idx]
}

func sq(n int) int { return n * n }

// parseHex reads "#rrggbb" (the form the webmail category editor stores).
func parseHex(s string) (r, g, b int, ok bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v>>16) & 0xFF, int(v>>8) & 0xFF, int(v) & 0xFF, true
}

func itoa(n int) string { return strconv.Itoa(n) }

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

// sortAttrs gives the encoder a stable attribute order, so an unchanged list
// re-encodes to the same bytes and a diff of two saves shows only real edits.
func sortAttrs(a []xml.Attr) {
	sort.SliceStable(a, func(i, j int) bool { return a[i].Name.Local < a[j].Name.Local })
}

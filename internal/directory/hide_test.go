package directory

import (
	"database/sql"
	"testing"
)

func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
func nsNull() sql.NullString     { return sql.NullString{} }

// TestHideMaskFromProps proves the two-source decode of the address-book hide
// mask: the PtLong mask form (parsed base-0) wins when present; absent that, the
// legacy boolean, when truthy, expands to "hidden from GAL and address lists"
// (0x03); anything unparsable or absent reads as visible. This is the contract
// the NSPI layer relies on to apply per-surface hide bits.
func TestHideMaskFromProps(t *testing.T) {
	cases := []struct {
		name    string
		mask    sql.NullString
		boolean sql.NullString
		want    uint32
	}{
		{"decimal mask", ns("3"), nsNull(), 0x03},
		{"hex mask", ns("0x09"), nsNull(), 0x09},
		{"mask wins over boolean", ns("1"), ns("1"), 0x01},
		{"boolean only, truthy expands to 0x03", nsNull(), ns("1"), 0x03},
		{"boolean only, false", nsNull(), ns("0"), 0},
		{"both absent", nsNull(), nsNull(), 0},
		{"empty mask falls back to boolean", ns(""), ns("1"), 0x03},
		{"unparsable mask, no boolean", ns("abc"), nsNull(), 0},
		{"padded mask is trimmed", ns(" 3 "), nsNull(), 0x03},
		// A mask that is not a PtLong must not unhide a user the boolean hides.
		{"unparsable mask falls back to boolean", ns("abc"), ns("1"), 0x03},
		{"negative mask falls back to boolean", ns("-1"), ns("1"), 0x03},
		{"mask over 32 bits falls back to boolean", ns("0x100000000"), ns("1"), 0x03},
		{"largest PtLong mask", ns("0xFFFFFFFF"), nsNull(), 0xFFFFFFFF},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hideMaskFromProps(c.mask, c.boolean); got != c.want {
				t.Errorf("hideMaskFromProps(%v, %v) = %#x, want %#x", c.mask, c.boolean, got, c.want)
			}
		})
	}
}

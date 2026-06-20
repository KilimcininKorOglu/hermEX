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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hideMaskFromProps(c.mask, c.boolean); got != c.want {
				t.Errorf("hideMaskFromProps(%v, %v) = %#x, want %#x", c.mask, c.boolean, got, c.want)
			}
		})
	}
}

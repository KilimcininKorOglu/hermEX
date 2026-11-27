package nspi

import (
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// PROPERTY_ROW flags ([MS-OXCDATA] 2.8.1).
const (
	propRowNone    uint8 = 0x00 // every column present; bare values follow
	propRowFlagged uint8 = 0x01 // a FLAGGED_PROPVAL per column follows
)

// allValues returns every column's value in column order, and false as soon as
// one column is missing from the bag.
func allValues(cols []mapi.PropTag, bag mapi.PropertyValues) ([]any, bool) {
	vals := make([]any, 0, len(cols))
	for _, c := range cols {
		v, ok := bag.Get(c)
		if !ok {
			return nil, false
		}
		vals = append(vals, v)
	}
	return vals, true
}

// pushPropertyRow serializes one column-projected PROPERTY_ROW ([MS-OXCDATA]
// 2.8.1) against cols, the shape QueryRows' row set uses: a NONE row (flag 0x00,
// a bare value per column) when every column is present, else a FLAGGED row
// (flag 0x01, a FLAGGED_PROPVAL per column, available with its value, or a
// PT_ERROR marker carrying ecNotFound when the column has no value, matching the
// reference's per-property fetch). The column proptag types each value, and
// because the push runs under EXT_FLAG_ABK the string/binary values carry the
// address-book presence marker. A wholly absent row (every column missing, e.g.
// an unknown MId) collapses to a FLAGGED row of all-error columns.
func pushPropertyRow(p *ext.Push, cols []mapi.PropTag, bag mapi.PropertyValues) error {
	if vals, ok := allValues(cols, bag); ok {
		p.Uint8(propRowNone)
		for i, c := range cols {
			if err := p.PropValue(c.Type(), vals[i]); err != nil {
				return err
			}
		}
		return nil
	}
	p.Uint8(propRowFlagged)
	for _, c := range cols {
		if v, ok := bag.Get(c); ok {
			if err := p.FlaggedPropVal(c, mapi.FlaggedPropVal{Flag: mapi.FlaggedAvailable, Value: v}); err != nil {
				return err
			}
		} else if err := p.FlaggedPropVal(c, mapi.FlaggedPropVal{Flag: mapi.FlaggedError, Value: ecNotFound}); err != nil {
			return err
		}
	}
	return nil
}

// pushColRow serializes a ROWSET ([MS-OXNSPI] 2.2.4): the column proptag array,
// the row count, then each row as a PROPERTY_ROW projected against the columns.
func pushColRow(p *ext.Push, cols []mapi.PropTag, rows []mapi.PropertyValues) error {
	if err := p.PropTagsLong(cols); err != nil {
		return err
	}
	// #nosec G115 -- a Go slice length; the buffer it measures is orders of magnitude below the field
	p.Uint32(uint32(len(rows)))
	for _, row := range rows {
		if err := pushPropertyRow(p, cols, row); err != nil {
			return err
		}
	}
	return nil
}

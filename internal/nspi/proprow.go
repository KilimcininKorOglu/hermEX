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

// pushPropertyRow serializes one column-projected PROPERTY_ROW ([MS-OXCDATA]
// 2.8.1) against cols, the shape QueryRows' row set uses: a NONE row (flag 0x00,
// a bare value per column) when every column is present, else a FLAGGED row
// (flag 0x01, a FLAGGED_PROPVAL per column — available with its value, or a
// PT_ERROR marker carrying ecNotFound when the column has no value, matching the
// reference's per-property fetch). The column proptag types each value, and
// because the push runs under EXT_FLAG_ABK the string/binary values carry the
// address-book presence marker. A wholly absent row (every column missing, e.g.
// an unknown MId) collapses to a FLAGGED row of all-error columns.
func pushPropertyRow(p *ext.Push, cols []mapi.PropTag, bag mapi.PropertyValues) error {
	allPresent := true
	for _, c := range cols {
		if _, ok := bag.Get(c); !ok {
			allPresent = false
			break
		}
	}
	if allPresent {
		p.Uint8(propRowNone)
		for _, c := range cols {
			v, _ := bag.Get(c)
			if err := p.PropValue(c.Type(), v); err != nil {
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
	p.Uint32(uint32(len(rows)))
	for _, row := range rows {
		if err := pushPropertyRow(p, cols, row); err != nil {
			return err
		}
	}
	return nil
}

package nspi

import (
	"errors"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// TestNDRRestrictionRejectsWideResType pins the discriminant width. The wire
// carries the restriction type in 32 bits and RestrictionType is 8, so 0x100 used
// to truncate to 0 (AND) and the node was then parsed with the AND layout instead
// of being refused as the unknown kind it is.
func TestNDRRestrictionRejectsWideResType(t *testing.T) {
	p := ndr.NewPush()
	p.Uint32(0x100)
	p.Uint32(0x100)
	p.Uint32(0) // whatever an AND frame would read next
	p.UniquePtr(false)

	_, err := pullRestrictionNDR(ndr.NewPull(p.Bytes()))
	if !errors.Is(err, ndr.ErrFormat) {
		t.Fatalf("restriction type 0x100 gave %v, want a format refusal", err)
	}
}

// TestNDRRestrictionRejectsWideRelop pins the comparison-operator width. Relop is
// 8 bits, so a wire value of 0x100 used to truncate into RelopLT and the filter
// silently ran a comparison the client never asked for.
func TestNDRRestrictionRejectsWideRelop(t *testing.T) {
	p := ndr.NewPush()
	p.Uint32(uint32(mapi.ResProperty))
	p.Uint32(uint32(mapi.ResProperty))
	p.Uint32(0x100)                      // relop
	p.Uint32(uint32(mapi.PrDisplayName)) // proptag
	p.UniquePtr(false)

	_, err := pullRestrictionNDR(ndr.NewPull(p.Bytes()))
	if !errors.Is(err, ndr.ErrFormat) {
		t.Fatalf("relop 0x100 gave %v, want a format refusal", err)
	}
}

// TestNDRPropValTypeCheckIsFullWidth pins the width of the PROPERTY_VALUE type
// check. The wire carries the type in its own 32-bit field while PropType is 16
// bits, so a truncating comparison accepted anything in the high half: 0x0001001F
// passed as PtUnicode.
func TestNDRPropValTypeCheckIsFullWidth(t *testing.T) {
	p := ndr.NewPush()
	p.Uint32(uint32(mapi.PrDisplayName)) // tag, type PtUnicode (0x001F)
	p.Uint32(0)                          // reserved
	p.Uint32(0x0001001F)                 // type field: right low half, junk high half
	p.UniquePtr(true)

	_, err := pullPropValNDR(ndr.NewPull(p.Bytes()))
	if !errors.Is(err, ndr.ErrFormat) {
		t.Fatalf("type field 0x0001001F gave %v, want a format refusal", err)
	}
}

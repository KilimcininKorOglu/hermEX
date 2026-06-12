package mapi

import "testing"

// The tag layout (id<<16 | type) is fixed by MS-OXCDATA; these tests pin it to
// real Exchange property tags so a regression in the bit math is caught.
// PR_SUBJECT_W = 0x0037001F and PR_SUBJECT_A = 0x0037001E share id 0x0037.
const (
	prSubjectW PropTag = 0x0037001F
	prSubjectA PropTag = 0x0037001E
)

func TestMakeTagMatchesKnownTags(t *testing.T) {
	if got := MakeTag(0x0037, PtUnicode); got != prSubjectW {
		t.Errorf("MakeTag(0x0037, PtUnicode) = 0x%08X, want 0x%08X", uint32(got), uint32(prSubjectW))
	}
	if got := MakeTag(0x0037, PtString8); got != prSubjectA {
		t.Errorf("MakeTag(0x0037, PtString8) = 0x%08X, want 0x%08X", uint32(got), uint32(prSubjectA))
	}
}

func TestPropTagDecompose(t *testing.T) {
	if id := prSubjectW.ID(); id != 0x0037 {
		t.Errorf("ID() = 0x%04X, want 0x0037", id)
	}
	if typ := prSubjectW.Type(); typ != PtUnicode {
		t.Errorf("Type() = %s, want PtUnicode", typ)
	}
}

func TestWithTypePreservesID(t *testing.T) {
	// Switching PR_SUBJECT_W to the 8-bit string type must yield PR_SUBJECT_A:
	// the id is preserved, only the type changes.
	if got := prSubjectW.WithType(PtString8); got != prSubjectA {
		t.Errorf("WithType(PtString8) = 0x%08X, want 0x%08X", uint32(got), uint32(prSubjectA))
	}
}

func TestTagComposeDecomposeRoundTrip(t *testing.T) {
	for _, id := range []uint16{0x0000, 0x0037, 0x3001, 0x8001, 0xFFFF} {
		for _, typ := range []PropType{PtUnicode, PtBinary, PtMvBinary, PtSysTime} {
			tag := MakeTag(id, typ)
			if tag.ID() != id || tag.Type() != typ {
				t.Errorf("round trip id=0x%04X typ=%s -> tag=0x%08X -> id=0x%04X typ=%s",
					id, typ, uint32(tag), tag.ID(), tag.Type())
			}
		}
	}
}

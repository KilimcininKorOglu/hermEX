package mapi

import "testing"

// The 0x1000 multivalue bit relates each PtMv* type to its scalar base; the
// wire encoder in package ext relies on Base() to pick the element codec, so a
// mistake here would corrupt every multivalue property.
func TestMultivalueFlagAndBase(t *testing.T) {
	cases := []struct {
		mv, base PropType
	}{
		{PtMvShort, PtShort}, {PtMvLong, PtLong}, {PtMvI8, PtI8},
		{PtMvString8, PtString8}, {PtMvUnicode, PtUnicode},
		{PtMvSysTime, PtSysTime}, {PtMvCLSID, PtCLSID}, {PtMvBinary, PtBinary},
	}
	for _, c := range cases {
		if !c.mv.IsMultivalue() {
			t.Errorf("%s.IsMultivalue() = false, want true", c.mv)
		}
		if c.base.IsMultivalue() {
			t.Errorf("%s.IsMultivalue() = true, want false", c.base)
		}
		if got := c.mv.Base(); got != c.base {
			t.Errorf("%s.Base() = %s, want %s", c.mv, got, c.base)
		}
		// The multivalue type must equal base | MvFlag.
		if c.base|MvFlag != c.mv {
			t.Errorf("%s | MvFlag = 0x%04X, want %s", c.base, uint16(c.base|MvFlag), c.mv)
		}
	}
}

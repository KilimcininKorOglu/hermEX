package activesync

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// TestBusyStatusRejectsWiderThanPtLong pins the parse width of the MS-ASCAL
// BusyStatus element. The value arrives from the device and lands in a PtLong,
// which is 32-bit, so a wider integer parsed at the platform width wrapped into a
// different, valid-looking status: 4294967298 became 2 (tentative) rather than
// being refused.
func TestBusyStatusRejectsWiderThanPtLong(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameBusyStatus})
	if err != nil {
		t.Fatal(err)
	}
	busyTag := mapi.MakeTag(ids[0], mapi.PtLong)

	cases := []struct {
		name string
		in   string
		want any // nil means the property must not be set at all
	}{
		{"in range", "2", int32(2)},
		{"wraps to a valid status", "4294967298", nil},
		{"above int32", "2147483648", nil},
		{"below int32", "-2147483649", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := wbxml.Elem(wbxml.ASData, wbxml.Str(wbxml.CalBusyStatus, tc.in))
			props, err := parseCalendarItem(st, data)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := props.Get(busyTag)
			if tc.want == nil {
				if ok {
					t.Fatalf("BusyStatus %q was accepted as %v", tc.in, got)
				}
				return
			}
			if !ok {
				t.Fatalf("BusyStatus %q was dropped", tc.in)
			}
			if got != tc.want {
				t.Errorf("BusyStatus %q = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

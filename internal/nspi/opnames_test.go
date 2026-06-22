package nspi

import "testing"

// TestOperationName proves the opnum→name map the activity log relies on: known
// opnums resolve to their NSPI operation name so the log is legible, and an
// unrecognized opnum falls back to a numeric form rather than an empty or wrong
// label that would hide a stray call.
func TestOperationName(t *testing.T) {
	cases := map[uint16]string{
		opNspiBind:          "Bind",
		opNspiUnbind:        "Unbind",
		opNspiQueryRows:     "QueryRows",
		opNspiGetProps:      "GetProps",
		opNspiResolveNames:  "ResolveNames",
		opNspiResolveNamesW: "ResolveNamesW",
	}
	for op, want := range cases {
		if got := OperationName(op); got != want {
			t.Errorf("OperationName(%d) = %q, want %q", op, got, want)
		}
	}
	// 99 is past the highest defined NSPI opnum: the fallback keeps it legible.
	if got := OperationName(99); got != "op99" {
		t.Errorf("OperationName(99) = %q, want op99 (numeric fallback)", got)
	}
}

package nspi

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// TestEncodersAnswerErrorForAnUnencodableRow proves a response whose row cannot be
// serialized (a value whose Go type does not match its property type) answers
// ecError with no row set, rather than a success whose row data stops short and
// leaves the client to read past the end of the frame.
func TestEncodersAnswerErrorForAnUnencodableRow(t *testing.T) {
	s := NewServer(nil, testGUID)
	cols := []mapi.PropTag{mapi.PrDisplayName}
	bad := mapi.PropertyValues{{Tag: mapi.PrDisplayName, Value: 42}}
	if err := ext.NewPush(abkFlags).PropertyValuesLong(bad); err == nil {
		t.Fatal("the bad row encodes; the test needs a value the encoder refuses")
	}
	st := stat{curRec: midBase, codePage: 1252}
	rows := []mapi.PropertyValues{bad}
	for name, resp := range map[string][]byte{
		"GetProps":        s.encodeGetProps(ecSuccess, 1252, bad),
		"GetMatches":      s.encodeGetMatches(ecSuccess, st, []uint32{midBase}, cols, rows),
		"ResolveNames":    s.encodeResolveNames(ecSuccess, 1252, []uint32{midBase}, cols, rows),
		"SeekEntries":     s.encodeSeekEntries(ecSuccess, st, cols, rows),
		"QueryRows":       s.encodeQueryRows(ecSuccess, st, cols, rows),
		"GetSpecialTable": s.encodeGetSpecialTable(ecSuccess, 1252, rows),
	} {
		p := ext.NewPull(resp, abkFlags)
		status := mustU32(t, p, name+" status")
		result := mustU32(t, p, name+" result")
		if status != 0 || result != ecError {
			t.Errorf("%s: (status %#x, result %#x), want (0, ecError)", name, status, result)
		}
	}
}

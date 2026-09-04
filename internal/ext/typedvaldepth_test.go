package ext

import (
	"bytes"
	"errors"
	"testing"

	"hermex/internal/mapi"
)

// TestTypedPropValRejectsSelfNesting is the stack-overflow guard. A value whose
// type is PtUnspecified is read back through TypedPropVal, so a run of zero bytes
// drives the two decoders into each other at one frame per two bytes. Unbounded,
// a few megabytes of attacker bytes exceed the goroutine stack, and a stack
// overflow is a runtime throw the daemon cannot recover from. The restriction
// depth counter does not cover this path, because the cycle never passes through
// Restriction.
func TestTypedPropValRejectsSelfNesting(t *testing.T) {
	// Every 0x0000 pair is another PtUnspecified frame.
	buf := bytes.Repeat([]byte{0x00, 0x00}, maxTypedValDepth+50)

	_, err := NewPull(buf, 0).PropValue(mapi.PtUnspecified)
	if !errors.Is(err, errTypedValTooDeep) {
		t.Fatalf("self-nesting value gave %v, want the depth-limit refusal", err)
	}
}

// TestTypedPropValKeepsOrdinaryValue is the control: the cap is far above any
// real value, so a normally typed self-describing value still decodes.
func TestTypedPropValKeepsOrdinaryValue(t *testing.T) {
	p := NewPush(0)
	if err := p.TypedPropVal(mapi.TypedPropVal{Type: mapi.PtLong, Value: int32(7)}); err != nil {
		t.Fatalf("push: %v", err)
	}

	v, err := NewPull(p.Bytes(), 0).TypedPropVal()
	if err != nil {
		t.Fatalf("ordinary typed value refused: %v", err)
	}
	if v.Type != mapi.PtLong {
		t.Fatalf("type = %#x, want PtLong", uint16(v.Type))
	}
	if n, _ := v.Value.(int32); n != 7 {
		t.Errorf("value = %v, want 7", v.Value)
	}
}

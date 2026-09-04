package oxtask

import (
	"math"
	"testing"

	"hermex/internal/mapi"
)

// staticResolver hands out a distinct id per requested name so ToProps writes
// every named property.
func staticResolver(_ bool, names []mapi.PropertyName) ([]uint16, error) {
	out := make([]uint16, len(names))
	for i := range names {
		out[i] = uint16(0x8000 + i)
	}
	return out, nil
}

// TestToPropsRejectsModelIntegersWiderThanPtLong pins the width of the task
// model's integer fields. They are Go ints filled by the protocol mappers from
// client input, and they are written into 32-bit MAPI longs, so a value past that
// width wrapped: an importance of 2147483650 was stored as -2147483646, and a
// value a multiple of 2^32 above a real setting became that setting exactly.
func TestToPropsRejectsModelIntegersWiderThanPtLong(t *testing.T) {
	const wide = int(math.MaxInt32) + 3 // wraps to -2147483646

	t.Run("importance", func(t *testing.T) {
		task := New()
		task.Importance = wide
		p, err := ToProps(task, staticResolver)
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := p.Get(mapi.PrImportance); ok {
			t.Errorf("importance %d was stored as %v", wide, v)
		}
	})

	t.Run("importance in range", func(t *testing.T) {
		task := New()
		task.Importance = 2
		p, err := ToProps(task, staticResolver)
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := p.Get(mapi.PrImportance); !ok || v != int32(2) {
			t.Errorf("importance = %v (ok=%v), want 2", v, ok)
		}
	})
}

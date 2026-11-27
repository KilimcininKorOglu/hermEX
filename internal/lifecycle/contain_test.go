package lifecycle

import (
	"errors"
	"strings"
	"testing"
)

// TestContainTurnsAPanicIntoAnError runs an item that panics. The caller must get
// an error it can identify as a contained panic, carrying the value raised and a
// stack that names where it was raised, and the goroutine must keep running.
func TestContainTurnsAPanicIntoAnError(t *testing.T) {
	err := Contain(func() error { panic("bad queue item") })
	if !errors.Is(err, ErrPanicked) {
		t.Fatalf("Contain = %v, want an error matching ErrPanicked", err)
	}
	var pe *PanicError
	if !errors.As(err, &pe) || pe.Value != "bad queue item" {
		t.Fatalf("Contain = %#v, want a *PanicError holding the panic value", err)
	}
	if !strings.Contains(string(pe.Stack), "TestContainTurnsAPanicIntoAnError") {
		t.Errorf("stack does not name the panicking item:\n%s", pe.Stack)
	}
	if strings.Contains(err.Error(), "goroutine") {
		t.Errorf("Error() carries the stack trace: %q", err.Error())
	}
}

// TestContainBoundsTheRecordedStack panics from deep inside a recursion, whose
// full trace is far larger than the bound. The *.panic event records the stack,
// so an unbounded copy would let one bad item write megabytes per occurrence into
// the log store.
func TestContainBoundsTheRecordedStack(t *testing.T) {
	var recurse func(n int) error
	recurse = func(n int) error {
		if n == 0 {
			panic("deep")
		}
		return recurse(n - 1)
	}

	var pe *PanicError
	if !errors.As(Contain(func() error { return recurse(20000) }), &pe) {
		t.Fatal("Contain did not return a *PanicError")
	}
	if len(pe.Stack) == 0 || len(pe.Stack) > panicStackBytes {
		t.Errorf("stack is %d bytes, want 1..%d", len(pe.Stack), panicStackBytes)
	}
}

// TestContainPassesThroughAnItemsOwnResult keeps an item that returns normally
// unchanged: its own error, or nil, is what the caller sees.
func TestContainPassesThroughAnItemsOwnResult(t *testing.T) {
	want := errors.New("remote refused")
	err := Contain(func() error { return want })
	if err != want {
		t.Errorf("Contain = %v, want the item's own error", err)
	}
	if errors.Is(err, ErrPanicked) {
		t.Errorf("Contain normal error unexpectedly matches ErrPanicked: %v", err)
	}
	if err := Contain(func() error { return nil }); err != nil {
		t.Errorf("Contain = %v, want nil", err)
	}
}

// TestContainRecoversNilItemAndPanicNil proves hostile inputs (a nil callback
// or panic(nil)) are contained as *PanicError wrapping ErrPanicked without
// breaking subsequent work items.
func TestContainRecoversNilItemAndPanicNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		item func() error
	}{
		{name: "nil callback", item: nil},
		{name: "panic nil", item: func() error { panic(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Contain(tc.item)
			if !errors.Is(err, ErrPanicked) {
				t.Fatalf("Contain = %v, want ErrPanicked", err)
			}
			var pe *PanicError
			if !errors.As(err, &pe) || len(pe.Stack) == 0 || len(pe.Stack) > panicStackBytes {
				t.Fatalf("Contain = %#v, want *PanicError with bounded non-empty stack", err)
			}
			if next := Contain(func() error { return nil }); next != nil {
				t.Errorf("subsequent Contain after recovery = %v, want nil", next)
			}
		})
	}
}

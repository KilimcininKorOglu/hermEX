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

// TestContainPassesThroughAnItemsOwnResult keeps an item that returns normally
// unchanged: its own error, or nil, is what the caller sees.
func TestContainPassesThroughAnItemsOwnResult(t *testing.T) {
	want := errors.New("remote refused")
	if err := Contain(func() error { return want }); err != want {
		t.Errorf("Contain = %v, want the item's own error", err)
	}
	if err := Contain(func() error { return nil }); err != nil {
		t.Errorf("Contain = %v, want nil", err)
	}
}

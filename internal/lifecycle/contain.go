package lifecycle

import (
	"errors"
	"fmt"
	"runtime"
)

// ErrPanicked marks an error Contain made from a panic.
var ErrPanicked = errors.New("lifecycle: work item panicked")

// PanicError is a panic Contain recovered: the value raised and the stack of the
// goroutine that raised it, bounded to panicStackBytes.
type PanicError struct {
	Value any
	Stack []byte
}

// Error names the panic value. The stack stays out of the message so a caller
// that records the error in a queue row does not store a trace there.
func (e *PanicError) Error() string { return fmt.Sprintf("%v: %v", ErrPanicked, e.Value) }

// Unwrap lets errors.Is(err, ErrPanicked) identify a contained panic.
func (e *PanicError) Unwrap() error { return ErrPanicked }

// Contain runs one item of a background pass and returns a panic it raises as a
// *PanicError. A background loop serves no client, so nothing recovers a panic
// for it the way net/http does for a handler: without this, one bad queue item
// stops the daemon, and because the item is still queued after the restart, it
// stops it again on every start. With it the item fails like any other attempt
// and the pass goes on to the next one.
func Contain(item func() error) (err error) {
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		buf := make([]byte, panicStackBytes)
		err = &PanicError{Value: v, Stack: buf[:runtime.Stack(buf, false)]}
	}()
	return item()
}

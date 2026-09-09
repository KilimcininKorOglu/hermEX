package rpchttp

import (
	"strconv"
	"testing"
)

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// wantTrue fails the test unless the condition holds.
func wantTrue(t *testing.T, got bool, what string) {
	t.Helper()
	if !got {
		t.Errorf("%s: false, want true", what)
	}
}

// wantFalse fails the test when the condition holds.
func wantFalse(t *testing.T, got bool, what string) {
	t.Helper()
	if got {
		t.Errorf("%s: true, want false", what)
	}
}

// mustNoErr stops the test on an error from a step under test.
func mustNoErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantCommands fails the test unless an RTS PDU decodes to exactly the given
// command types, in order.
func wantCommands(t *testing.T, cmds []rtsCommand, what string, types ...uint32) {
	t.Helper()
	if len(cmds) != len(types) {
		t.Fatalf("%s = %v, want %d commands", what, cmds, len(types))
	}
	for i, typ := range types {
		wantEq(t, cmds[i].Type, typ, what+" command "+strconv.Itoa(i)+" type")
	}
}

package rpchttp

import (
	"io"
	"strconv"
	"testing"

	"hermex/internal/ndr"
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

// mustU32 reads one NDR uint32 and stops the test when the stub is too short, so a
// truncated reply fails as truncated instead of reading as a zero field.
func mustU32(t *testing.T, p *ndr.Pull, what string) uint32 {
	t.Helper()
	v, err := p.Uint32()
	mustNoErr(t, err, "read "+what)
	return v
}

// mustU16 reads one NDR uint16, stopping the test on a short stub.
func mustU16(t *testing.T, p *ndr.Pull, what string) uint16 {
	t.Helper()
	v, err := p.Uint16()
	mustNoErr(t, err, "read "+what)
	return v
}

// mustU8 reads one NDR byte, stopping the test on a short stub.
func mustU8(t *testing.T, p *ndr.Pull, what string) uint8 {
	t.Helper()
	v, err := p.Uint8()
	mustNoErr(t, err, "read "+what)
	return v
}

// mustRaw reads n raw bytes, stopping the test on a short stub.
func mustRaw(t *testing.T, p *ndr.Pull, n int, what string) []byte {
	t.Helper()
	b, err := p.Raw(n)
	mustNoErr(t, err, "read "+what)
	return b
}

// mustWrite sends one PDU on a channel body, stopping the test when the write
// fails, so a closed channel shows as a write failure rather than a missing reply.
func mustWrite(t *testing.T, w io.Writer, pdu []byte, what string) {
	t.Helper()
	_, err := w.Write(pdu)
	mustNoErr(t, err, "write "+what)
}

// mustHeader parses a PDU's common header, stopping the test on a malformed PDU.
func mustHeader(t *testing.T, pdu []byte) ndr.Header {
	t.Helper()
	h, err := ndr.ParseHeader(pdu)
	mustNoErr(t, err, "parse the PDU header")
	return h
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

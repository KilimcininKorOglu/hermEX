package rop

import (
	"encoding/hex"
	"testing"
)

// Dispatch either recognizes an opcode or it does not, and the difference is
// invisible in an otherwise passing suite: an unrecognized opcode gets the
// 6-byte generic error and ENDS THE BATCH, so a client's remaining ROPs are
// dropped unanswered rather than processed. Nothing else in this package checks
// which opcodes are wired at all.
//
// This is a characterization test. The opcode set below was recorded from the
// implementation, not derived from [MS-OXCROPS]; its job is to prove that a
// restructuring of the dispatch kept the same opcodes answering.

// wiredROPs is every opcode Dispatch answers itself rather than falling through
// to the generic error, probed with a bare header and no request body.
//
// Eleven wired opcodes are absent, because their handler answers a bodiless
// request with the same generic error an unknown opcode gets and the two are
// indistinguishable from outside: 0x1B, 0x47, 0x59, 0x5A, 0x5D, 0x5E, 0x68,
// 0x6C, 0x6D, 0x77 and 0x81. Those are covered by the dispatch table's own
// completeness test rather than from the wire.
var wiredROPs = []uint8{
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
	0x0A, 0x0B, 0x0C, 0x0E, 0x10, 0x11, 0x12, 0x13,
	0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1C,
	0x1D, 0x1E, 0x1F, 0x20, 0x21, 0x22, 0x23, 0x24,
	0x25, 0x26, 0x27, 0x29, 0x2B, 0x2C, 0x2D, 0x2E,
	0x2F, 0x30, 0x31, 0x32, 0x33, 0x35, 0x36, 0x37,
	0x38, 0x39, 0x3E, 0x3F, 0x40, 0x41, 0x43, 0x44,
	0x46, 0x4A, 0x4B, 0x4C, 0x4D, 0x4E, 0x4F, 0x50,
	0x53, 0x54, 0x55, 0x56, 0x58, 0x60, 0x61, 0x63,
	0x64, 0x66, 0x67, 0x69, 0x6B, 0x70, 0x72, 0x73,
	0x74, 0x75, 0x76, 0x78, 0x7A, 0x7B, 0x7E, 0x7F,
	0x80, 0x82, 0x89, 0x91, 0x92, 0x93, 0xFE,
}

// unknownReply is what an opcode Dispatch does not recognize answers: RopId,
// HandleIndex, then ecError little-endian.
func unknownReply(op uint8) string {
	return hex.EncodeToString([]byte{op, 0, 0x05, 0x40, 0x00, 0x80})
}

// dispatchProbe runs one opcode with an empty body against a fresh session and
// returns the response as hex.
func dispatchProbe(t *testing.T, op uint8) string {
	t.Helper()
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()
	resp, _ := sess.Dispatch([]byte{op, 0, 0}, []uint32{0xFFFFFFFF})
	return hex.EncodeToString(resp)
}

// TestDispatchAnswersTheSameOpcodeSet walks the whole 8-bit opcode space and
// asserts exactly which values Dispatch answers itself. A wiring lost in a
// refactor turns a recognized opcode into an unrecognized one, and a wiring
// added by mistake does the reverse; both fail here.
func TestDispatchAnswersTheSameOpcodeSet(t *testing.T) {
	wired := make(map[uint8]bool, len(wiredROPs))
	for _, op := range wiredROPs {
		wired[op] = true
	}
	for op := 0; op <= 0xFF; op++ {
		code := uint8(op)
		answered := dispatchProbe(t, code) != unknownReply(code)
		if answered != wired[code] {
			t.Errorf("opcode %#x: answered by Dispatch = %v, want %v", code, answered, wired[code])
		}
	}
}

// tableOnlyROPs are the wired opcodes a bodiless probe cannot see, because their
// handler answers such a request with the same generic error an unknown opcode
// gets. They are checked against the dispatch table directly, which is the only
// place the difference is visible.
var tableOnlyROPs = []uint8{
	ropCreateBookmark, ropSetSpooler, ropExpandRow, ropCollapseRow,
	ropCommitStream, ropGetStreamSize, ropGetReceiveFolderTable,
	ropSetCollapseState, ropGetTransportFolder, ropSyncUploadStateStreamEnd,
	ropResetTable,
}

// TestDispatchTableCoversEveryWiredOpcode is the companion to the wire probe: it
// asserts the table holds exactly the opcodes the probe found plus the eleven it
// cannot distinguish. Together the two cover every wiring.
func TestDispatchTableCoversEveryWiredOpcode(t *testing.T) {
	want := make(map[uint8]bool, len(wiredROPs)+len(tableOnlyROPs))
	for _, op := range wiredROPs {
		want[op] = true
	}
	for _, op := range tableOnlyROPs {
		want[op] = true
	}
	if len(ropTable) != len(want) {
		t.Errorf("the table holds %d opcodes, want %d", len(ropTable), len(want))
	}
	for op := range want {
		if _, ok := ropTable[op]; !ok {
			t.Errorf("opcode %#x is not in the dispatch table", op)
		}
	}
	for op := range ropTable {
		if !want[op] {
			t.Errorf("the dispatch table holds %#x, which neither list names", op)
		}
	}
}

// TestDispatchStopsAtAnUnknownOpcode pins the batch-ending contract: a ROP list
// is a stream with no per-ROP length, so an opcode Dispatch cannot parse leaves
// the reader at an unknown offset and every following ROP is abandoned. A
// refactor that answered the error and kept reading would emit responses parsed
// out of another ROP's body.
func TestDispatchStopsAtAnUnknownOpcode(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	// An unknown opcode followed by a well-formed RopLogon.
	list := append([]byte{0x00, 0, 0}, logonRequest(0, 0x01)...)
	resp, handles := sess.Dispatch(list, []uint32{0xFFFFFFFF})

	if got, want := hex.EncodeToString(resp), unknownReply(0x00); got != want {
		t.Errorf("response = %s, want only the generic error %s", got, want)
	}
	if handles[0] != 0xFFFFFFFF {
		t.Errorf("the logon behind the unknown opcode ran: handle = %#x", handles[0])
	}
}

// TestDispatchStopsAtATruncatedHeader pins the other batch-ending path: a ROP
// header is three bytes and a short one cannot name an opcode, so the batch ends
// with no error response at all.
func TestDispatchStopsAtATruncatedHeader(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	resp, _ := sess.Dispatch([]byte{ropRelease, 0}, []uint32{0xFFFFFFFF})
	if len(resp) != 0 {
		t.Errorf("a truncated header produced %d response bytes, want none: %x", len(resp), resp)
	}
}

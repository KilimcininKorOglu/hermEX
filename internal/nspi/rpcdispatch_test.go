package nspi

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// testServerGUID is the stable server identity an RPC NspiBind returns.
var testServerGUID = mapi.GUID{
	Data1: 0x11223344, Data2: 0x5566, Data3: 0x7788,
	Data4: [8]byte{0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00},
}

// buildBindIN frames an NspiBind request stub: flags, the STAT, and a non-null
// [in,out] server-GUID pointer carrying the client's (ignored) cached value.
func buildBindIN(flags, codePage uint32) []byte {
	p := ndr.NewPush()
	p.Uint32(flags)
	pushStatNDR(p, stat{codePage: codePage})
	p.UniquePtr(true)
	p.Raw(make([]byte, 16)) // cached server GUID, ignored by the server
	return p.Bytes()
}

// bindResult pulls a bind OUT and returns the result code and the handle GUID. It
// assumes a present server-GUID pointer, which buildBindIN always sends.
func bindResult(t *testing.T, out []byte) (uint32, mapi.GUID) {
	t.Helper()
	q := ndr.NewPull(out)
	if _, err := q.Uint32(); err != nil { // server GUID referent
		t.Fatalf("referent: %v", err)
	}
	if _, err := q.Raw(16); err != nil { // server GUID flat bytes
		t.Fatalf("server guid: %v", err)
	}
	_, g, err := pullCtxHandleNDR(q)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	r, err := q.Uint32()
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	return r, g
}

// TestRPCBindRoundTrip drives a legal NspiBind through DispatchRPC and pulls the
// OUT: the [in,out] server GUID is present and equals this server's GUID, the
// minted context handle is non-zero, and the result is ecSuccess.
func TestRPCBindRoundTrip(t *testing.T) {
	s := NewServer(nil, testServerGUID)
	out, fault := s.DispatchRPC(opNspiBind, buildBindIN(0, 1252))
	if fault != 0 {
		t.Fatalf("bind fault = %#x, want 0", fault)
	}
	q := ndr.NewPull(out)
	ref, err := q.Uint32()
	if err != nil || ref == 0 {
		t.Fatalf("server GUID referent = %#x (err %v), want non-zero", ref, err)
	}
	gotGUID, err := q.Raw(16)
	if err != nil {
		t.Fatalf("read server GUID: %v", err)
	}
	wantGUID := testServerGUID.Flat()
	if !bytes.Equal(gotGUID, wantGUID[:]) {
		t.Errorf("server GUID = %x, want %x", gotGUID, wantGUID[:])
	}
	handleType, handleGUID, err := pullCtxHandleNDR(q)
	if err != nil {
		t.Fatalf("read ctx handle: %v", err)
	}
	if handleType != 0 || handleGUID == (mapi.GUID{}) {
		t.Errorf("handle = {type %d, guid %+v}, want type 0 + non-zero guid", handleType, handleGUID)
	}
	result, err := q.Uint32()
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if result != ecSuccess {
		t.Errorf("result = %#x, want ecSuccess", result)
	}
}

// TestRPCBindMintsDistinctHandles proves two binds get different handle GUIDs, so
// a client opening multiple bindings never collides.
func TestRPCBindMintsDistinctHandles(t *testing.T) {
	s := NewServer(nil, testServerGUID)
	g1 := bindHandleGUID(t, s)
	g2 := bindHandleGUID(t, s)
	if g1 == g2 {
		t.Errorf("two binds returned the same handle GUID %+v", g1)
	}
}

func bindHandleGUID(t *testing.T, s *Server) mapi.GUID {
	t.Helper()
	out, fault := s.DispatchRPC(opNspiBind, buildBindIN(0, 1252))
	if fault != 0 {
		t.Fatalf("bind fault %#x", fault)
	}
	_, g := bindResult(t, out)
	return g
}

// TestRPCBindAnonymousRejected proves an anonymous bind is refused with a zeroed
// handle, matching the MAPI/HTTP admission check.
func TestRPCBindAnonymousRejected(t *testing.T) {
	s := NewServer(nil, testServerGUID)
	out, fault := s.DispatchRPC(opNspiBind, buildBindIN(fAnonymousLogin, 1252))
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	result, handleGUID := bindResult(t, out)
	if result != ecNotSupported {
		t.Errorf("anonymous result = %#x, want ecNotSupported", result)
	}
	if handleGUID != (mapi.GUID{}) {
		t.Errorf("anonymous handle guid = %+v, want zero", handleGUID)
	}
}

// TestRPCBindUnicodeRejected proves a Unicode code page is refused (NSPI strings
// are code-page encoded, so the bind cannot proceed in Unicode).
func TestRPCBindUnicodeRejected(t *testing.T) {
	s := NewServer(nil, testServerGUID)
	out, fault := s.DispatchRPC(opNspiBind, buildBindIN(0, cpWinUnicode))
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	result, _ := bindResult(t, out)
	if result != ecNotSupported {
		t.Errorf("unicode result = %#x, want ecNotSupported", result)
	}
}

// TestRPCUnbind proves NspiUnbind returns a zeroed handle and MAPI_E_UNBINDSUCCESS
// (1), the NSPI-specific success code rather than ecSuccess.
func TestRPCUnbind(t *testing.T) {
	s := NewServer(nil, testServerGUID)
	p := ndr.NewPush()
	pushCtxHandleNDR(p, 0, testServerGUID) // a live handle the client holds
	p.Uint32(0)                            // reserved
	out, fault := s.DispatchRPC(opNspiUnbind, p.Bytes())
	if fault != 0 {
		t.Fatalf("unbind fault %#x", fault)
	}
	q := ndr.NewPull(out)
	handleType, handleGUID, err := pullCtxHandleNDR(q)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if handleType != 0 || handleGUID != (mapi.GUID{}) {
		t.Errorf("unbind handle = {type %d, guid %+v}, want zeroed", handleType, handleGUID)
	}
	result, err := q.Uint32()
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if result != nspiUnbindSuccess {
		t.Errorf("unbind result = %#x, want MAPI_E_UNBINDSUCCESS (1)", result)
	}
}

// TestRPCUnsupportedOpnum proves an opnum the dispatch does not handle returns an
// op-range fault, not a silent empty response. opnum 99 is well past the highest
// defined NSPI operation (ResolveNamesW = 20), so it is unambiguously unhandled — no
// reserved-vs-defined question (the reference reserves 15/17/18 in-range and faults
// them too). The write/template opnums 11/13/14 no longer fault — they answer with a
// faithful MAPI error (covered by the write-range tests in rpcdata_test.go).
func TestRPCUnsupportedOpnum(t *testing.T) {
	s := NewServer(nil, testServerGUID)
	out, fault := s.DispatchRPC(99, nil) // 99: past the highest defined NSPI opnum
	if fault != ndr.FaultOpRngError {
		t.Errorf("opnum 99 fault = %#x, want FaultOpRngError", fault)
	}
	if out != nil {
		t.Errorf("opnum 99 out = %x, want nil", out)
	}
}

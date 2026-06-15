package nspi

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// testGUID is a fixed server GUID so a Bind response can be asserted exactly.
var testGUID = mapi.GUID{Data1: 0x11223344, Data2: 0x5566, Data3: 0x7788, Data4: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}

// TestStatRoundTrip proves the 36-byte STAT layout: every field survives a
// push/pull cycle in order, including the signed delta.
func TestStatRoundTrip(t *testing.T) {
	in := stat{
		sortType: 1, containerID: 0, curRec: 0x10, delta: -5,
		numPos: 3, totalRec: 42, codePage: 1252, tplLocale: 0x0409, sortLocale: 0x0409,
	}
	p := ext.NewPush(abkFlags)
	pushStat(p, in)
	if got := len(p.Bytes()); got != 36 {
		t.Fatalf("STAT encoded to %d bytes, want 36", got)
	}
	out, err := pullStat(ext.NewPull(p.Bytes(), abkFlags))
	if err != nil {
		t.Fatalf("pullStat: %v", err)
	}
	if out != in {
		t.Errorf("STAT round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

// buildBind frames a Bind request body: flags + optional STAT + an auxiliary
// buffer (here empty). codePage is only written when withStat is set.
func buildBind(flags uint32, withStat bool, codePage uint32) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(flags)
	if withStat {
		p.Uint8(1)
		pushStat(p, stat{codePage: codePage})
	} else {
		p.Uint8(0)
	}
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// decodeBindResp reads a Bind response: status + result + server GUID + auxout.
func decodeBindResp(t *testing.T, resp []byte) (status, result uint32, guid mapi.GUID) {
	t.Helper()
	p := ext.NewPull(resp, abkFlags)
	status, _ = p.Uint32()
	result, _ = p.Uint32()
	g, err := p.GUID()
	if err != nil {
		t.Fatalf("decode server GUID: %v", err)
	}
	aux, _ := p.Uint32()
	if aux != 0 {
		t.Errorf("AuxiliaryBufferSize = %d, want 0", aux)
	}
	if p.Remaining() != 0 {
		t.Errorf("Bind response has %d trailing bytes", p.Remaining())
	}
	return status, result, g
}

// TestBindSuccess proves a plain Bind succeeds, reports the server GUID, and
// signals the transport to establish the session.
func TestBindSuccess(t *testing.T) {
	s := NewServer(nil, testGUID)
	resp, ok := s.Bind(buildBind(0, false, 0))
	if !ok {
		t.Fatal("Bind ok = false, want true")
	}
	status, result, guid := decodeBindResp(t, resp)
	if status != 0 {
		t.Errorf("status = %#x, want 0", status)
	}
	if result != ecSuccess {
		t.Errorf("result = %#x, want ecSuccess", result)
	}
	if guid != testGUID {
		t.Errorf("server GUID = %+v, want %+v", guid, testGUID)
	}
}

// TestBindRejectsAnonymous proves an anonymous bind fails and carries a zeroed
// server GUID, and the transport is told not to open a session.
func TestBindRejectsAnonymous(t *testing.T) {
	s := NewServer(nil, testGUID)
	resp, ok := s.Bind(buildBind(fAnonymousLogin, false, 0))
	if ok {
		t.Fatal("anonymous Bind ok = true, want false")
	}
	_, result, guid := decodeBindResp(t, resp)
	if result != ecNotSupported {
		t.Errorf("result = %#x, want ecNotSupported", result)
	}
	if (guid != mapi.GUID{}) {
		t.Errorf("failed Bind carried a non-zero server GUID: %+v", guid)
	}
}

// TestBindRejectsUnicodeCodepage proves a CP_WINUNICODE bind is refused (NSPI
// strings are code-page encoded), guarding the codepage check.
func TestBindRejectsUnicodeCodepage(t *testing.T) {
	s := NewServer(nil, testGUID)
	_, ok := s.Bind(buildBind(0, true, cpWinUnicode))
	if ok {
		t.Fatal("Unicode-codepage Bind ok = true, want false")
	}
}

// TestUnbindSucceeds proves Unbind returns a well-formed success response.
func TestUnbindSucceeds(t *testing.T) {
	s := NewServer(nil, testGUID)
	resp := s.Unbind([]byte{0, 0, 0, 0, 0, 0, 0, 0}) // reserved u32 + cb_auxin u32
	p := ext.NewPull(resp, abkFlags)
	status, _ := p.Uint32()
	result, _ := p.Uint32()
	aux, _ := p.Uint32()
	if status != 0 || result != ecSuccess || aux != 0 {
		t.Errorf("Unbind response = (status %#x, result %#x, aux %d), want (0, 0, 0)", status, result, aux)
	}
	if p.Remaining() != 0 {
		t.Errorf("Unbind response has %d trailing bytes", p.Remaining())
	}
}

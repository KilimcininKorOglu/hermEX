package rpchttp

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/ndr"
	"hermex/internal/oxmapihttp"
)

// buildRpcExt2Stub assembles an EcDoRpcExt2 request stub carrying a ROP buffer.
func buildRpcExt2Stub(cxh ContextHandle, pin []byte) []byte {
	p := ndr.NewPush()
	pushCtxHandle(p, cxh)
	p.Uint32(0)                // flags
	p.Uint32(uint32(len(pin))) // pin max_count
	p.Raw(pin)
	p.Uint32(uint32(len(pin))) // cb_in
	p.Uint32(0x40000)          // cb_out (offered)
	p.Uint32(0)                // AUX-in max_count
	p.Uint32(0)                // cb_auxin
	p.Uint32(0)                // cb_auxout
	return p.Bytes()
}

// parseRpcExt2Out decodes an EcDoRpcExt2 response: the echoed handle, the ROP
// response buffer, and the result.
func parseRpcExt2Out(t *testing.T, stub []byte) (ContextHandle, []byte, uint32) {
	t.Helper()
	p := ndr.NewPull(stub)
	cxh, _ := pullCtxHandle(p)
	p.Uint32()          // flags
	mc, _ := p.Uint32() // cb_out max_count
	p.Uint32()          // offset
	p.Uint32()          // actual_count
	pout, _ := p.Raw(int(mc))
	p.Uint32()           // cb_out (redundant)
	amc, _ := p.Uint32() // AUX max_count
	p.Uint32()           // offset
	p.Uint32()           // actual_count
	p.Raw(int(amc))
	p.Uint32() // cb_auxout (redundant)
	p.Uint32() // trans_time
	result, _ := p.Uint32()
	return cxh, pout, result
}

// logonExecuteBuffer builds the ROP buffer Outlook carries in EcDoRpcExt2 for a
// private-mailbox RopLogon: the ROP-command region plus the handle table, wrapped
// in the RPC_HEADER_EXT envelope.
func logonExecuteBuffer() []byte {
	rb := ext.NewPush(ext.FlagUTF16)
	rb.Uint8(0xFE) // RopLogon ([MS-OXCROPS] 2.2.3.1)
	rb.Uint8(0)    // LogonId
	rb.Uint8(0)    // OutputHandleIndex
	rb.Uint8(0x01) // LogonFlags = Private
	rb.Uint32(0)   // OpenFlags
	rb.Uint32(0)   // StoreState
	rb.Uint16(0)   // EssdnSize (none; the session is keyed by the mailbox)
	return oxmapihttp.EncodeExecute(rb.Bytes(), []uint32{0xFFFFFFFF})
}

// TestRpcExt2Logon proves EcDoRpcExt2 carries a ROP buffer byte-for-byte through
// the same decode/dispatch/encode path the MAPI/HTTP Execute uses: a RopLogon
// against a fresh mailbox returns a RopLogon response (ReturnValue 0) inside the
// wrapped output buffer, and the context handle is echoed.
func TestRpcExt2Logon(t *testing.T) {
	ems := NewEMSMDB(nil)
	// Connect against a fresh on-disk mailbox; the ROP store opens at RopLogon.
	out, _ := ems.Handle(&Session{User: "alice@hermex.test", Mailbox: t.TempDir()}, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"))
	cxh, _ := parseConnectExOut(t, out)

	resp, fault := ems.Handle(&Session{}, opEcDoRpcExt2, buildRpcExt2Stub(cxh, logonExecuteBuffer()))
	if fault != 0 {
		t.Fatalf("EcDoRpcExt2 fault = %#x", fault)
	}
	gotCxh, pout, result := parseRpcExt2Out(t, resp)
	if result != ecSuccess {
		t.Fatalf("EcDoRpcExt2 result = %#x, want ecSuccess", result)
	}
	if gotCxh.GUID != cxh.GUID {
		t.Errorf("EcDoRpcExt2 did not echo the context handle")
	}

	rops, _, err := oxmapihttp.DecodeExecute(pout)
	if err != nil {
		t.Fatalf("decode ROP response: %v", err)
	}
	rp := ext.NewPull(rops, ext.FlagUTF16)
	ropID, _ := rp.Uint8()
	rp.Uint8()           // OutputHandleIndex
	rv, _ := rp.Uint32() // ReturnValue
	if ropID != 0xFE || rv != ecSuccess {
		t.Errorf("ROP response = (RopId %#x, ReturnValue %#x), want (0xFE, 0) — a RopLogon success", ropID, rv)
	}
}

// TestRpcExt2UnknownHandleFaults proves EcDoRpcExt2 with an unknown context
// handle faults (context mismatch) rather than acting on a missing session.
func TestRpcExt2UnknownHandleFaults(t *testing.T) {
	ems := NewEMSMDB(nil)
	bogus := ContextHandle{GUID: mapi.GUID{Data1: 0xDEAD}}
	if _, fault := ems.Handle(&Session{}, opEcDoRpcExt2, buildRpcExt2Stub(bogus, nil)); fault != ndr.FaultContextMismatch {
		t.Errorf("unknown-handle fault = %#x, want context mismatch", fault)
	}
}

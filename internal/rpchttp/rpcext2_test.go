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

// openFolderExecuteBuffer builds the ROP buffer for a RopOpenFolder(Inbox) that
// resolves the logon handle a prior Execute minted (slot 0) and places the opened
// folder in slot 1.
func openFolderExecuteBuffer(logonH uint32) []byte {
	rb := ext.NewPush(ext.FlagUTF16)
	rb.Uint8(0x02) // RopOpenFolder ([MS-OXCROPS] 2.2.4.1)
	rb.Uint8(0)    // LogonId
	rb.Uint8(0)    // InputHandleIndex (slot 0 = logon handle)
	rb.Uint8(1)    // OutputHandleIndex (slot 1 = opened folder)
	rb.Uint64(uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox)))
	rb.Uint8(0) // OpenModeFlags
	return oxmapihttp.EncodeExecute(rb.Bytes(), []uint32{logonH, 0xFFFFFFFF})
}

// TestRpcExt2CrossExecuteHandle proves the EcDoRpcExt2 session keyed by the
// context handle persists the ROP object/handle table across separate Execute
// calls: a RopLogon in the first Execute mints a logon handle, and a RopOpenFolder
// in a second Execute on the same context handle resolves that handle to open the
// Inbox. Both calls go through ems.Handle, so it is the cxh -> session lookup
// (distinct code from the MAPI/HTTP sid-cookie map) that is exercised; if that
// lookup ever stopped persisting handles, the second Execute's OpenFolder would
// fail to resolve the logon handle and return ecError.
func TestRpcExt2CrossExecuteHandle(t *testing.T) {
	ems := NewEMSMDB(nil)
	out, _ := ems.Handle(&Session{User: "alice@hermex.test", Mailbox: t.TempDir()}, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"))
	cxh, _ := parseConnectExOut(t, out)

	// Execute #1: RopLogon -> the logon handle lands in the response handle table.
	resp, fault := ems.Handle(&Session{}, opEcDoRpcExt2, buildRpcExt2Stub(cxh, logonExecuteBuffer()))
	if fault != 0 {
		t.Fatalf("logon EcDoRpcExt2 fault = %#x", fault)
	}
	_, pout, result := parseRpcExt2Out(t, resp)
	if result != ecSuccess {
		t.Fatalf("logon result = %#x, want ecSuccess", result)
	}
	_, handles, err := oxmapihttp.DecodeExecute(pout)
	if err != nil {
		t.Fatalf("decode logon response: %v", err)
	}
	if len(handles) != 1 || handles[0] == 0xFFFFFFFF {
		t.Fatalf("logon did not mint a handle: %v", handles)
	}
	logonH := handles[0]

	// Execute #2 on the same context handle: RopOpenFolder(Inbox) reusing the logon
	// handle the first Execute minted.
	resp, fault = ems.Handle(&Session{}, opEcDoRpcExt2, buildRpcExt2Stub(cxh, openFolderExecuteBuffer(logonH)))
	if fault != 0 {
		t.Fatalf("open-folder EcDoRpcExt2 fault = %#x", fault)
	}
	_, pout, result = parseRpcExt2Out(t, resp)
	if result != ecSuccess {
		t.Fatalf("open-folder EcDoRpcExt2 result = %#x, want ecSuccess", result)
	}
	rops, handles, err := oxmapihttp.DecodeExecute(pout)
	if err != nil {
		t.Fatalf("decode open-folder response: %v", err)
	}
	rp := ext.NewPull(rops, ext.FlagUTF16)
	ropID, _ := rp.Uint8()
	rp.Uint8()           // OutputHandleIndex
	rv, _ := rp.Uint32() // ReturnValue
	if ropID != 0x02 || rv != ecSuccess {
		t.Fatalf("open-folder response = (RopId %#x, ReturnValue %#x), want (0x02, 0) — the logon handle did not survive into the second Execute", ropID, rv)
	}
	if len(handles) != 2 || handles[1] == 0xFFFFFFFF {
		t.Fatalf("open-folder did not mint a folder handle in slot 1: %v", handles)
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

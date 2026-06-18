package rpchttp

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/ndr"
	"hermex/internal/objectstore"
	"hermex/internal/oxmapihttp"
)

// buildAsyncConnectExStub assembles an EcDoAsyncConnectEx request: just the session
// context handle.
func buildAsyncConnectExStub(cxh ContextHandle) []byte {
	p := ndr.NewPush()
	pushCtxHandle(p, cxh)
	return p.Bytes()
}

// parseAsyncConnectExOut decodes an EcDoAsyncConnectEx response: the async context
// handle and the result.
func parseAsyncConnectExOut(t *testing.T, stub []byte) (ContextHandle, uint32) {
	t.Helper()
	p := ndr.NewPull(stub)
	acxh, err := pullCtxHandle(p)
	if err != nil {
		t.Fatalf("async ctx handle: %v", err)
	}
	result, _ := p.Uint32()
	return acxh, result
}

// buildAsyncWaitExStub assembles an EcDoAsyncWaitEx request: the async context
// handle plus the reserved flags word.
func buildAsyncWaitExStub(acxh ContextHandle) []byte {
	p := ndr.NewPush()
	pushCtxHandle(p, acxh)
	p.Uint32(0) // flags_in (reserved)
	return p.Bytes()
}

// parseAsyncWaitExOut decodes an EcDoAsyncWaitEx response: flags_out + result.
func parseAsyncWaitExOut(t *testing.T, stub []byte) (flagsOut, result uint32) {
	t.Helper()
	p := ndr.NewPull(stub)
	flagsOut, _ = p.Uint32()
	result, _ = p.Uint32()
	return flagsOut, result
}

// registerNotifyExecuteBuffer builds a ROP buffer for a whole-store
// RopRegisterNotification(fnevObjectCreated) on the logon handle in slot 0, placing
// the subscription in slot 1.
func registerNotifyExecuteBuffer(logonH uint32) []byte {
	rb := ext.NewPush(ext.FlagUTF16)
	rb.Uint8(0x29) // RopRegisterNotification ([MS-OXCNOTIF] 2.2.1.2.1)
	rb.Uint8(0)    // LogonId
	rb.Uint8(0)    // InputHandleIndex (slot 0 = logon handle)
	rb.Uint8(1)    // OutputHandleIndex (slot 1 = the subscription)
	rb.Uint8(0x04) // NotificationTypes = fnevObjectCreated
	rb.Uint8(0)    // Reserved
	rb.Uint8(1)    // WantWholeStore
	return oxmapihttp.EncodeExecute(rb.Bytes(), []uint32{logonH, 0xFFFFFFFF})
}

// TestAsyncConnectEx proves EcDoAsyncConnectEx converts a session context handle
// into an async handle that carries the same GUID re-tagged with the async
// discriminator, and that an unknown handle faults rather than minting one.
func TestAsyncConnectEx(t *testing.T) {
	ems := NewEMSMDB(nil)
	const user = "alice@hermex.test"
	out, _ := ems.Handle(&Session{User: user, Mailbox: t.TempDir()}, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"))
	cxh, _ := parseConnectExOut(t, out)

	aout, fault := ems.Handle(&Session{User: user}, opEcDoAsyncConnectEx, buildAsyncConnectExStub(cxh))
	if fault != 0 {
		t.Fatalf("EcDoAsyncConnectEx fault = %#x", fault)
	}
	acxh, result := parseAsyncConnectExOut(t, aout)
	if result != ecSuccess {
		t.Errorf("EcDoAsyncConnectEx result = %#x, want ecSuccess", result)
	}
	if acxh.GUID != cxh.GUID {
		t.Errorf("async handle GUID = %v, want the session GUID %v", acxh.GUID, cxh.GUID)
	}
	if acxh.HandleType != asyncHandleType {
		t.Errorf("async handle type = %d, want the async discriminator %d", acxh.HandleType, asyncHandleType)
	}

	bogus := ContextHandle{GUID: mapi.GUID{Data1: 0xDEAD}}
	if _, fault := ems.Handle(&Session{User: user}, opEcDoAsyncConnectEx, buildAsyncConnectExStub(bogus)); fault != ndr.FaultContextMismatch {
		t.Errorf("unknown-handle async connect fault = %#x, want context mismatch", fault)
	}
}

// TestAsyncWaitExRejectsForeignHandles proves EcDoAsyncWaitEx rejects an async
// handle naming no live session, or one owned by another authenticated user — the
// reference's username guard against waiting on someone else's mailbox.
func TestAsyncWaitExRejectsForeignHandles(t *testing.T) {
	ems := NewEMSMDB(nil)
	async := NewAsyncEMSMDB(ems)
	const user = "alice@hermex.test"
	out, _ := ems.Handle(&Session{User: user, Mailbox: t.TempDir()}, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"))
	cxh, _ := parseConnectExOut(t, out)
	aout, _ := ems.Handle(&Session{User: user}, opEcDoAsyncConnectEx, buildAsyncConnectExStub(cxh))
	acxh, _ := parseAsyncConnectExOut(t, aout)

	// Unknown handle.
	bogus := ContextHandle{HandleType: asyncHandleType, GUID: mapi.GUID{Data1: 0xDEAD}}
	wout, fault := async.Handle(&Session{User: user}, opEcDoAsyncWaitEx, buildAsyncWaitExStub(bogus))
	if fault != 0 {
		t.Fatalf("async wait fault = %#x", fault)
	}
	if flags, result := parseAsyncWaitExOut(t, wout); flags != 0 || result != ecRejected {
		t.Errorf("unknown-handle wait = (flags %#x, result %#x), want (0, ecRejected)", flags, result)
	}

	// Valid handle, wrong user.
	wout, _ = async.Handle(&Session{User: "bob@hermex.test"}, opEcDoAsyncWaitEx, buildAsyncWaitExStub(acxh))
	if flags, result := parseAsyncWaitExOut(t, wout); flags != 0 || result != ecRejected {
		t.Errorf("foreign-user wait = (flags %#x, result %#x), want (0, ecRejected)", flags, result)
	}
}

// TestAsyncWaitExNoNotification proves a wait on a quiet mailbox returns
// flags_out 0 (no notification pending) with ecSuccess — the value the long-poll
// also returns on timeout.
func TestAsyncWaitExNoNotification(t *testing.T) {
	ems := NewEMSMDB(nil)
	async := NewAsyncEMSMDB(ems)
	const user = "alice@hermex.test"
	out, _ := ems.Handle(&Session{User: user, Mailbox: t.TempDir()}, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"))
	cxh, _ := parseConnectExOut(t, out)
	aout, _ := ems.Handle(&Session{User: user}, opEcDoAsyncConnectEx, buildAsyncConnectExStub(cxh))
	acxh, _ := parseAsyncConnectExOut(t, aout)

	wout, fault := async.Handle(&Session{User: user}, opEcDoAsyncWaitEx, buildAsyncWaitExStub(acxh))
	if fault != 0 {
		t.Fatalf("async wait fault = %#x", fault)
	}
	if flags, result := parseAsyncWaitExOut(t, wout); flags != 0 || result != ecSuccess {
		t.Errorf("quiet-mailbox wait = (flags %#x, result %#x), want (0, ecSuccess)", flags, result)
	}
}

// TestAsyncWaitExNotificationPending proves the async wait reflects the shared
// store: after a whole-store subscription is registered and a message is delivered
// into the mailbox through a SEPARATE store handle (as another daemon's MTA would),
// EcDoAsyncWaitEx reports FLAG_NOTIFICATION_PENDING. This closes the async path to
// the same PollForChange wake primitive the MAPI/HTTP NotificationWait uses.
func TestAsyncWaitExNotificationPending(t *testing.T) {
	ems := NewEMSMDB(nil)
	async := NewAsyncEMSMDB(ems)
	const user = "alice@hermex.test"
	mailbox := t.TempDir()
	out, _ := ems.Handle(&Session{User: user, Mailbox: mailbox}, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"))
	cxh, _ := parseConnectExOut(t, out)

	// Logon opens the ROP store; its response handle table carries the logon handle.
	lresp, fault := ems.Handle(&Session{}, opEcDoRpcExt2, buildRpcExt2Stub(cxh, logonExecuteBuffer()))
	if fault != 0 {
		t.Fatalf("logon fault = %#x", fault)
	}
	_, lpout, _ := parseRpcExt2Out(t, lresp)
	_, lhandles, derr := oxmapihttp.DecodeExecute(lpout)
	if derr != nil {
		t.Fatalf("decode logon response: %v", derr)
	}
	if len(lhandles) != 1 || lhandles[0] == 0xFFFFFFFF {
		t.Fatalf("logon did not mint a handle: %v", lhandles)
	}

	// Register a whole-store notification on the logon handle.
	resp, fault := ems.Handle(&Session{}, opEcDoRpcExt2, buildRpcExt2Stub(cxh, registerNotifyExecuteBuffer(lhandles[0])))
	if fault != 0 {
		t.Fatalf("register-notification fault = %#x", fault)
	}
	if _, _, result := parseRpcExt2Out(t, resp); result != ecSuccess {
		t.Fatalf("register-notification result = %#x, want ecSuccess", result)
	}

	aout, _ := ems.Handle(&Session{User: user}, opEcDoAsyncConnectEx, buildAsyncConnectExStub(cxh))
	acxh, _ := parseAsyncConnectExOut(t, aout)

	// A quiet wait first: nothing pending yet.
	wout, _ := async.Handle(&Session{User: user}, opEcDoAsyncWaitEx, buildAsyncWaitExStub(acxh))
	if flags, _ := parseAsyncWaitExOut(t, wout); flags != 0 {
		t.Fatalf("wait before delivery = flags %#x, want 0", flags)
	}

	// A message is delivered into the mailbox through a separate store handle, as a
	// separate delivering daemon would.
	st, err := objectstore.Open(mailbox)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}

	wout, fault = async.Handle(&Session{User: user}, opEcDoAsyncWaitEx, buildAsyncWaitExStub(acxh))
	if fault != 0 {
		t.Fatalf("async wait fault = %#x", fault)
	}
	if flags, result := parseAsyncWaitExOut(t, wout); flags != flagNotificationPending || result != ecSuccess {
		t.Errorf("wait after delivery = (flags %#x, result %#x), want (FLAG_NOTIFICATION_PENDING, ecSuccess)", flags, result)
	}
}

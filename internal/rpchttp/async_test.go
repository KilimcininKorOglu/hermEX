package rpchttp

import (
	"sync"
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
	cxh, _ := connectLogonRegister(t, ems, user, mailbox)

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

	wout, fault := async.Handle(&Session{User: user}, opEcDoAsyncWaitEx, buildAsyncWaitExStub(acxh))
	if fault != 0 {
		t.Fatalf("async wait fault = %#x", fault)
	}
	if flags, result := parseAsyncWaitExOut(t, wout); flags != flagNotificationPending || result != ecSuccess {
		t.Errorf("wait after delivery = (flags %#x, result %#x), want (FLAG_NOTIFICATION_PENDING, ecSuccess)", flags, result)
	}
}

// connectLogonRegister drives a session to the point a whole-store notification is
// registered: EcDoConnectEx, an EcDoRpcExt2 RopLogon (which opens the ROP store), and
// an EcDoRpcExt2 RopRegisterNotification on the logon handle. It returns the context
// handle and the logon handle.
func connectLogonRegister(t *testing.T, ems *EMSMDB, user, mailbox string) (ContextHandle, uint32) {
	t.Helper()
	out, _ := ems.Handle(&Session{User: user, Mailbox: mailbox}, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"))
	cxh, _ := parseConnectExOut(t, out)

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

	resp, fault := ems.Handle(&Session{}, opEcDoRpcExt2, buildRpcExt2Stub(cxh, registerNotifyExecuteBuffer(lhandles[0])))
	if fault != 0 {
		t.Fatalf("register-notification fault = %#x", fault)
	}
	if _, _, result := parseRpcExt2Out(t, resp); result != ecSuccess {
		t.Fatalf("register-notification result = %#x, want ecSuccess", result)
	}
	return cxh, lhandles[0]
}

// testVconn builds a virtual connection a parked wait can deliver its reply on,
// without the full RTS handshake — the OUT channel is read directly.
func testVconn() *vconn {
	return &vconn{out: make(chan []byte, 16), closed: make(chan struct{})}
}

// readParkedReply reads one PDU off the OUT channel and returns its response stub,
// failing if none arrives.
func readParkedReply(t *testing.T, vc *vconn) []byte {
	t.Helper()
	select {
	case pdu := <-vc.out:
		return responseStub(t, pdu)
	case <-time.After(2 * time.Second):
		t.Fatal("no parked reply delivered on the OUT channel")
		return nil
	}
}

// asyncConnect runs EcDoAsyncConnectEx and returns the async context handle.
func asyncConnect(t *testing.T, ems *EMSMDB, user string, cxh ContextHandle) ContextHandle {
	t.Helper()
	aout, _ := ems.Handle(&Session{User: user}, opEcDoAsyncConnectEx, buildAsyncConnectExStub(cxh))
	acxh, _ := parseAsyncConnectExOut(t, aout)
	return acxh
}

// TestAsyncWaitExParksThenWakesOnDelivery proves the long-poll: a wait on a quiet
// mailbox parks (returns faultParked, no synchronous reply), and the parked poller
// delivers FLAG_NOTIFICATION_PENDING on the OUT channel once a message lands through a
// separate store handle. A second wait while one is parked is dropped immediately, so
// only one poller runs per session.
func TestAsyncWaitExParksThenWakesOnDelivery(t *testing.T) {
	ems := NewEMSMDB(nil)
	async := NewAsyncEMSMDB(ems)
	async.cadence = 10 * time.Millisecond
	async.waitInterval = 2 * time.Second
	const user = "alice@hermex.test"
	mailbox := t.TempDir()
	cxh, _ := connectLogonRegister(t, ems, user, mailbox)
	acxh := asyncConnect(t, ems, user, cxh)

	vc := testVconn()
	sess := &Session{User: user, vc: vc, curCallID: 0x99, maxFrag: 4096}
	if out, fault := async.asyncWaitEx(sess, buildAsyncWaitExStub(acxh)); fault != faultParked {
		t.Fatalf("quiet-mailbox wait = (out %v, fault %#x), want it to park", out, fault)
	}

	// A second wait while one is parked is dropped (no second poller), returning 0.
	if out2, fault2 := async.asyncWaitEx(&Session{User: user, vc: testVconn(), maxFrag: 4096}, buildAsyncWaitExStub(acxh)); fault2 == faultParked {
		t.Error("a second wait parked a second poller; want a dropped immediate reply")
	} else if flags, result := parseAsyncWaitExOut(t, out2); flags != 0 || result != ecSuccess {
		t.Errorf("dropped second wait = (flags %#x, result %#x), want (0, ecSuccess)", flags, result)
	}

	// Deliver a message through a separate store handle; the parked poller wakes.
	st, err := objectstore.Open(mailbox)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0); err != nil {
		t.Fatalf("append: %v", err)
	}

	if flags, result := parseAsyncWaitExOut(t, readParkedReply(t, vc)); flags != flagNotificationPending || result != ecSuccess {
		t.Errorf("parked reply = (flags %#x, result %#x), want (FLAG_NOTIFICATION_PENDING, ecSuccess)", flags, result)
	}
}

// TestAsyncWaitExParkTimesOut proves a parked wait with nothing pending delivers a
// flags_out 0 reply when the wait interval elapses — the timeout the client renews.
func TestAsyncWaitExParkTimesOut(t *testing.T) {
	ems := NewEMSMDB(nil)
	async := NewAsyncEMSMDB(ems)
	async.cadence = 10 * time.Millisecond
	async.waitInterval = 60 * time.Millisecond
	const user = "alice@hermex.test"
	cxh, _ := connectLogonRegister(t, ems, user, t.TempDir())
	acxh := asyncConnect(t, ems, user, cxh)

	vc := testVconn()
	if _, fault := async.asyncWaitEx(&Session{User: user, vc: vc, maxFrag: 4096}, buildAsyncWaitExStub(acxh)); fault != faultParked {
		t.Fatalf("quiet-mailbox wait did not park")
	}
	if flags, result := parseAsyncWaitExOut(t, readParkedReply(t, vc)); flags != 0 || result != ecSuccess {
		t.Errorf("timed-out parked reply = (flags %#x, result %#x), want (0, ecSuccess)", flags, result)
	}
}

// TestAsyncWaitExParkAbortsOnTeardown proves the parked poller abandons its reply and
// frees the per-session wait slot when the virtual connection tears down, rather than
// polling a closing store for the full wait interval.
func TestAsyncWaitExParkAbortsOnTeardown(t *testing.T) {
	ems := NewEMSMDB(nil)
	async := NewAsyncEMSMDB(ems)
	async.cadence = 10 * time.Millisecond
	async.waitInterval = 10 * time.Second // long, so only teardown ends the wait
	const user = "alice@hermex.test"
	cxh, _ := connectLogonRegister(t, ems, user, t.TempDir())
	acxh := asyncConnect(t, ems, user, cxh)
	s, _ := ems.lookup(cxh.GUID)

	vc := testVconn()
	if _, fault := async.asyncWaitEx(&Session{User: user, vc: vc, maxFrag: 4096}, buildAsyncWaitExStub(acxh)); fault != faultParked {
		t.Fatalf("quiet-mailbox wait did not park")
	}
	close(vc.closed) // tear the connection down

	deadline := time.Now().Add(2 * time.Second)
	for s.waiting.Load() {
		if time.Now().After(deadline) {
			t.Fatal("parked poller did not exit after teardown")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case pdu := <-vc.out:
		t.Errorf("teardown should abandon the reply, got %d bytes on the OUT channel", len(pdu))
	default:
	}
}

// TestAsyncWaitExConcurrentParkAndExecute runs a parked poll concurrently with
// Executes on the same session — the poller's PollForChange against the Executes'
// Dispatch on the shared rop.Session. Under `go test -race` it proves the session
// mutex guards that concurrency, mirroring the MAPI/HTTP NotificationWait race test.
func TestAsyncWaitExConcurrentParkAndExecute(t *testing.T) {
	ems := NewEMSMDB(nil)
	async := NewAsyncEMSMDB(ems)
	async.cadence = 2 * time.Millisecond
	async.waitInterval = 500 * time.Millisecond
	const user = "alice@hermex.test"
	mailbox := t.TempDir()
	cxh, logonH := connectLogonRegister(t, ems, user, mailbox)
	acxh := asyncConnect(t, ems, user, cxh)

	vc := testVconn()
	defer close(vc.closed)
	if _, fault := async.asyncWaitEx(&Session{User: user, vc: vc, maxFrag: 4096}, buildAsyncWaitExStub(acxh)); fault != faultParked {
		t.Fatalf("quiet-mailbox wait did not park")
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 20 {
			ems.Handle(&Session{}, opEcDoRpcExt2, buildRpcExt2Stub(cxh, openFolderExecuteBuffer(logonH)))
		}
	})
	wg.Go(func() {
		st, err := objectstore.Open(mailbox)
		if err != nil {
			return
		}
		defer st.Close()
		st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	})
	wg.Wait()

	// Drain the parked reply if the delivery woke it (either outcome is race-clean).
	select {
	case <-vc.out:
	case <-time.After(time.Second):
	}
}

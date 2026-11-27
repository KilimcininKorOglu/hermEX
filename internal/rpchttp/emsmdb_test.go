package rpchttp

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
	"hermex/internal/objectstore"
)

// provisionedDir returns a fresh directory holding a created mailbox, the state
// account creation leaves, so a test session can log on to it.
func provisionedDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// buildConnectExStub assembles an EcDoConnectEx request stub: the conformant-
// varying szUserDN string, the scalar parameters, and an empty AUX-in buffer.
func buildConnectExStub(userDN string) []byte {
	p := ndr.NewPush()
	dn := append([]byte(userDN), 0)
	p.Uint32(uint32(len(dn))) // userdn max_count
	p.Uint32(0)               // offset
	p.Uint32(uint32(len(dn))) // actual_count
	p.Raw(dn)
	p.Uint32(0)      // flags
	p.Uint32(0)      // conmod
	p.Uint32(0)      // limit
	p.Uint32(1252)   // cpid
	p.Uint32(0x041F) // lcid_string
	p.Uint32(0x0409) // lcid_sort
	p.Uint32(0)      // cxr_link
	p.Uint16(0)      // cnvt_cps
	p.Uint16(0)      // client_vers[0]
	p.Uint16(0)      // client_vers[1]
	p.Uint16(0)      // client_vers[2]
	p.Uint32(0)      // timestamp
	p.Uint32(0)      // AUX-in max_count
	p.Uint32(0)      // cb_auxin
	p.Uint32(0)      // cb_auxout
	return p.Bytes()
}

// TestConnectExReadsTheLocale proves EcDoConnectEx keeps the code page and the two
// language ids the client sent ([MS-OXCRPC] 3.1.4.1), which the logon reports as
// its locale properties.
func TestConnectExReadsTheLocale(t *testing.T) {
	in, err := pullConnectEx(buildConnectExStub("/o=hermex/cn=alice"))
	if err != nil {
		t.Fatal(err)
	}
	if in.cpid != 1252 || in.lcidString != 0x041F || in.lcidSort != 0x0409 {
		t.Errorf("cpid, lcid_string, lcid_sort = %d, %#x, %#x; want 1252, 0x41f, 0x409", in.cpid, in.lcidString, in.lcidSort)
	}
}

// pullConfString reads a unique-pointer conformant-varying string.
func pullConfString(t *testing.T, p *ndr.Pull) string {
	t.Helper()
	mustU32(t, p, "string referent id")
	mustU32(t, p, "string max_count")
	mustU32(t, p, "string offset")
	n := mustU32(t, p, "string actual_count")
	return trimNUL(mustRaw(t, p, int(n), "string bytes"))
}

// parseConnectExOut decodes an EcDoConnectEx response stub, returning the minted
// context handle and the result code.
func parseConnectExOut(t *testing.T, stub []byte) (ContextHandle, uint32) {
	t.Helper()
	p := ndr.NewPull(stub)
	cxh, err := pullCtxHandle(p)
	if err != nil {
		t.Fatalf("ctx handle: %v", err)
	}
	mustU32(t, p, "max_polls")
	mustU32(t, p, "max_retry")
	mustU32(t, p, "retry_delay")
	mustU16(t, p, "cxr")
	pullConfString(t, p)
	pullConfString(t, p)
	for range 6 { // server + best versions
		mustU16(t, p, "version word")
	}
	mustU32(t, p, "timestamp")
	mc := mustU32(t, p, "AUX max_count")
	mustU32(t, p, "AUX offset")
	mustU32(t, p, "AUX actual_count")
	mustRaw(t, p, int(mc), "AUX bytes")
	mustU32(t, p, "cb_auxout")
	return cxh, mustU32(t, p, "connect result")
}

// TestConnectDisconnect proves the EMSMDB stub mints a context handle on
// EcDoConnectEx, records the session, and frees it on EcDoDisconnect.
func TestConnectDisconnect(t *testing.T) {
	ems := NewEMSMDB(nil)
	sess := &Session{User: "alice@hermex.test", Mailbox: "/mb/alice"}

	out, fault := ems.Handle(sess, opEcDoConnectEx, buildConnectExStub("/o=hermex/ou=hermex/cn=Recipients/cn=alice@hermex.test"))
	if fault != 0 {
		t.Fatalf("connect fault = %#x", fault)
	}
	cxh, result := parseConnectExOut(t, out)
	if result != ecSuccess {
		t.Errorf("connect result = %#x, want ecSuccess", result)
	}
	if cxh.GUID == (mapi.GUID{}) {
		t.Error("connect returned a zero context handle")
	}
	if _, ok := ems.lookup("alice@hermex.test", cxh.GUID); !ok {
		t.Error("session not recorded after connect")
	}

	dstub := ndr.NewPush()
	pushCtxHandle(dstub, cxh)
	dout, fault := ems.Handle(sess, opEcDoDisconnect, dstub.Bytes())
	if fault != 0 {
		t.Fatalf("disconnect fault = %#x", fault)
	}
	dp := ndr.NewPull(dout)
	if _, err := pullCtxHandle(dp); err != nil {
		t.Fatal(err)
	}
	if dres := mustU32(t, dp, "disconnect result"); dres != ecSuccess {
		t.Errorf("disconnect result = %#x, want ecSuccess", dres)
	}
	if _, ok := ems.lookup("alice@hermex.test", cxh.GUID); ok {
		t.Error("session still present after disconnect")
	}
}

// TestUnknownOpnumFaults proves an unimplemented EMSMDB opnum faults.
func TestUnknownOpnumFaults(t *testing.T) {
	ems := NewEMSMDB(nil)
	if _, fault := ems.Handle(&Session{}, 99, nil); fault != ndr.FaultOpRngError {
		t.Errorf("unknown opnum fault = %#x, want op_rng_error", fault)
	}
}

// mustReq builds a request or fails the test setup.
func mustReq(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestEndToEndConnect drives the full RPC/HTTP vertical: it opens both channels
// over HTTP, binds the EMSMDB interface, runs EcDoConnectEx and EcDoDisconnect,
// and checks each step's reply on the OUT channel. This proves the transport,
// the dispatch engine, and the EMSMDB stub compose over real HTTP before the ROP
// path is stacked on top.
func TestEndToEndConnect(t *testing.T) {
	ems := NewEMSMDB(nil)
	disp := NewDispatcher()
	disp.Register(EMSMDBUUID, EMSMDBVersion, ems.Handle)
	srv := httptest.NewServer(NewServer(Config{Auth: okAuth, Dispatch: disp.Dispatch}))
	defer srv.Close()
	url := srv.URL + "/rpc/rpcproxy.dll?testhost:6001"

	outResp, err := http.DefaultClient.Do(mustReq(t, "RPC_OUT_DATA", url, bytes.NewReader(connA1())))
	mustNoErr(t, err, "OUT request")
	defer outResp.Body.Close()
	_, err = readPDU(outResp.Body) // CONN/A3
	mustNoErr(t, err, "read CONN/A3")

	pr, pw := io.Pipe()
	inDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(mustReq(t, "RPC_IN_DATA", url, pr))
		if resp != nil {
			resp.Body.Close()
		}
		inDone <- err
	}()
	mustWrite(t, pw, connB1(), "CONN/B1")
	_, err = readPDU(outResp.Body) // CONN/C2
	mustNoErr(t, err, "read CONN/C2")

	// Bind the EMSMDB interface.
	mustWrite(t, pw, buildBindPDU(0x30, EMSMDBUUID, EMSMDBVersion, 0), "the bind")
	wantPDUType(t, readReply(t, outResp.Body, "bind_ack"), ndr.PktBindAck, "bind reply")

	// EcDoConnectEx.
	mustWrite(t, pw, buildRequestPDU(0x31, 0, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"), ndr.PfcFirstFrag|ndr.PfcLastFrag), "EcDoConnectEx")
	connResp := readReply(t, outResp.Body, "connect response")
	wantPDUType(t, connResp, ndr.PktResponse, "connect reply")
	cxh, result := parseConnectExOut(t, responseStub(t, connResp))
	wantEq(t, result, uint32(ecSuccess), "connect result")
	wantTrue(t, cxh.GUID != (mapi.GUID{}), "the connect reply carries a context handle")

	// EcDoDisconnect.
	ds := ndr.NewPush()
	pushCtxHandle(ds, cxh)
	mustWrite(t, pw, buildRequestPDU(0x32, 0, opEcDoDisconnect, ds.Bytes(), ndr.PfcFirstFrag|ndr.PfcLastFrag), "EcDoDisconnect")
	wantPDUType(t, readReply(t, outResp.Body, "disconnect response"), ndr.PktResponse, "disconnect reply")
	_, stillOpen := ems.lookup("alice@hermex.test", cxh.GUID)
	wantFalse(t, stillOpen, "the session survives an end-to-end disconnect")

	_ = pw.Close()
	if err := <-inDone; err != nil {
		t.Errorf("RPC_IN_DATA request: %v", err)
	}
}

// readReply reads one PDU off the OUT channel, stopping the test when the channel
// gives nothing.
func readReply(t *testing.T, body io.Reader, what string) []byte {
	t.Helper()
	pdu, err := readPDU(body)
	mustNoErr(t, err, "read "+what)
	return pdu
}

// wantPDUType fails the test unless a PDU carries the expected packet type.
func wantPDUType(t *testing.T, pdu []byte, typ uint8, what string) {
	t.Helper()
	h, err := ndr.ParseHeader(pdu)
	mustNoErr(t, err, "parse "+what+" header")
	wantEq(t, h.Type, typ, what+" type")
}

// TestIdleSessionIsReclaimed proves an abandoned Outlook Anywhere session does
// not pin its mailbox forever. Only a clean EcDoDisconnect ever removed one, and
// a client that crashes, sleeps or loses its network path never sends one, so the
// ROP session and the open mailbox store (with its advisory lock) stayed for the
// process's life and every reconnect added another.
func TestIdleSessionIsReclaimed(t *testing.T) {
	ems := NewEMSMDB(nil)
	sess := &Session{User: "alice@hermex.test", Mailbox: provisionedDir(t)}

	out, fault := ems.Handle(sess, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"))
	if fault != 0 {
		t.Fatalf("connect fault = %#x", fault)
	}
	cxh, result := parseConnectExOut(t, out)
	wantEq(t, result, uint32(ecSuccess), "connect result")

	// Not yet idle: a sweep must leave a session the client is still using.
	if n := ems.Sweep(time.Now(), time.Hour); n != 0 {
		t.Fatalf("swept %d live session(s)", n)
	}
	if _, ok := ems.lookup("alice@hermex.test", cxh.GUID); !ok {
		t.Fatal("a live session was dropped")
	}

	// Idle past the window: reclaimed, and the handle no longer resolves.
	if n := ems.Sweep(time.Now().Add(2*time.Hour), time.Hour); n != 1 {
		t.Fatalf("swept %d session(s), want 1", n)
	}
	if _, ok := ems.lookup("alice@hermex.test", cxh.GUID); ok {
		t.Error("the abandoned session is still in the table")
	}
}

// TestSessionTouchDefersReclaim proves the sweep keys on real use: a client that
// keeps issuing calls must not have its session pulled out from under it.
func TestSessionTouchDefersReclaim(t *testing.T) {
	ems := NewEMSMDB(nil)
	sess := &Session{User: "alice@hermex.test", Mailbox: provisionedDir(t)}
	out, fault := ems.Handle(sess, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"))
	wantEq(t, fault, uint32(0), "connect fault")
	cxh, result := parseConnectExOut(t, out)
	wantEq(t, result, uint32(ecSuccess), "connect result")

	// Age the session past the window, as an hour of silence would.
	ems.mu.Lock()
	ems.sessions[cxh.GUID].lastSeen = time.Now().Add(-time.Hour)
	ems.mu.Unlock()

	// Another account naming the handle is refused and must not keep it alive.
	if _, ok := ems.lookup("bob@hermex.test", cxh.GUID); ok {
		t.Fatal("another account resolved alice's handle")
	}
	ems.mu.Lock()
	stale := time.Since(ems.sessions[cxh.GUID].lastSeen) >= time.Minute
	ems.mu.Unlock()
	wantTrue(t, stale, "a refused lookup by another account left the session stale")

	// The owner's call restamps the session, so a sweep with the window finds it fresh.
	if _, ok := ems.lookup("alice@hermex.test", cxh.GUID); !ok {
		t.Fatal("session missing")
	}
	if n := ems.Sweep(time.Now(), time.Minute); n != 0 {
		t.Errorf("swept %d session(s) the owner just used", n)
	}
}

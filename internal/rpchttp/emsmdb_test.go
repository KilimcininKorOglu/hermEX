package rpchttp

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// buildConnectExStub assembles an EcDoConnectEx request stub: the conformant-
// varying szUserDN string, the scalar parameters, and an empty AUX-in buffer.
func buildConnectExStub(userDN string) []byte {
	p := ndr.NewPush()
	dn := append([]byte(userDN), 0)
	p.Uint32(uint32(len(dn))) // userdn max_count
	p.Uint32(0)               // offset
	p.Uint32(uint32(len(dn))) // actual_count
	p.Raw(dn)
	p.Uint32(0)    // flags
	p.Uint32(0)    // conmod
	p.Uint32(0)    // limit
	p.Uint32(1252) // cpid
	p.Uint32(0)    // lcid_string
	p.Uint32(0)    // lcid_sort
	p.Uint32(0)    // cxr_link
	p.Uint16(0)    // cnvt_cps
	p.Uint16(0)    // client_vers[0]
	p.Uint16(0)    // client_vers[1]
	p.Uint16(0)    // client_vers[2]
	p.Uint32(0)    // timestamp
	p.Uint32(0)    // AUX-in max_count
	p.Uint32(0)    // cb_auxin
	p.Uint32(0)    // cb_auxout
	return p.Bytes()
}

// pullConfString reads a unique-pointer conformant-varying string.
func pullConfString(p *ndr.Pull) string {
	p.Uint32() // referent id
	p.Uint32() // max_count
	p.Uint32() // offset
	n, _ := p.Uint32()
	b, _ := p.Raw(int(n))
	return trimNUL(b)
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
	p.Uint32() // max_polls
	p.Uint32() // max_retry
	p.Uint32() // retry_delay
	p.Uint16() // cxr
	pullConfString(p)
	pullConfString(p)
	for range 6 { // server + best versions
		p.Uint16()
	}
	p.Uint32()          // timestamp
	mc, _ := p.Uint32() // AUX max_count
	p.Uint32()          // offset
	p.Uint32()          // actual_count
	p.Raw(int(mc))      // AUX bytes
	p.Uint32()          // cb_auxout (redundant)
	result, _ := p.Uint32()
	return cxh, result
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
	if _, ok := ems.lookup(cxh.GUID); !ok {
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
	if dres, _ := dp.Uint32(); dres != ecSuccess {
		t.Errorf("disconnect result = %#x, want ecSuccess", dres)
	}
	if _, ok := ems.lookup(cxh.GUID); ok {
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
	if err != nil {
		t.Fatalf("OUT request: %v", err)
	}
	defer outResp.Body.Close()
	if _, err := readPDU(outResp.Body); err != nil { // CONN/A3
		t.Fatalf("read CONN/A3: %v", err)
	}

	pr, pw := io.Pipe()
	inDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(mustReq(t, "RPC_IN_DATA", url, pr))
		if resp != nil {
			resp.Body.Close()
		}
		inDone <- err
	}()
	pw.Write(connB1())
	if _, err := readPDU(outResp.Body); err != nil { // CONN/C2
		t.Fatalf("read CONN/C2: %v", err)
	}

	// Bind the EMSMDB interface.
	pw.Write(buildBindPDU(0x30, EMSMDBUUID, EMSMDBVersion, 0))
	bindAck, err := readPDU(outResp.Body)
	if err != nil {
		t.Fatalf("read bind_ack: %v", err)
	}
	if h, _ := ndr.ParseHeader(bindAck); h.Type != ndr.PktBindAck {
		t.Fatalf("bind reply type = %#x, want BIND_ACK", h.Type)
	}

	// EcDoConnectEx.
	pw.Write(buildRequestPDU(0x31, 0, opEcDoConnectEx, buildConnectExStub("/o=hermex/cn=alice"), ndr.PfcFirstFrag|ndr.PfcLastFrag))
	connResp, err := readPDU(outResp.Body)
	if err != nil {
		t.Fatalf("read connect response: %v", err)
	}
	if h, _ := ndr.ParseHeader(connResp); h.Type != ndr.PktResponse {
		t.Fatalf("connect reply type = %#x, want RESPONSE", h.Type)
	}
	cxh, result := parseConnectExOut(t, responseStub(t, connResp))
	if result != ecSuccess || cxh.GUID == (mapi.GUID{}) {
		t.Fatalf("end-to-end connect = (result %#x, cxh %v), want (0, non-zero)", result, cxh.GUID)
	}

	// EcDoDisconnect.
	ds := ndr.NewPush()
	pushCtxHandle(ds, cxh)
	pw.Write(buildRequestPDU(0x32, 0, opEcDoDisconnect, ds.Bytes(), ndr.PfcFirstFrag|ndr.PfcLastFrag))
	discResp, err := readPDU(outResp.Body)
	if err != nil {
		t.Fatalf("read disconnect response: %v", err)
	}
	if h, _ := ndr.ParseHeader(discResp); h.Type != ndr.PktResponse {
		t.Errorf("disconnect reply type = %#x, want RESPONSE", h.Type)
	}
	if _, ok := ems.lookup(cxh.GUID); ok {
		t.Error("session still present after end-to-end disconnect")
	}

	pw.Close()
	<-inDone
}

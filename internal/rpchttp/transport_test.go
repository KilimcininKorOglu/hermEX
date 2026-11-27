package rpchttp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// okAuth is a test authenticator that always succeeds.
func okAuth(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	return "alice@hermex.test", "/mb/alice", true
}

var (
	testConnCookie = mapi.GUID{Data1: 0xAAAAAAAA, Data2: 0x1111, Data3: 0x2222, Data4: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}
	testChanCookie = mapi.GUID{Data1: 0xBBBBBBBB, Data2: 0x3333, Data3: 0x4444, Data4: [8]byte{9, 10, 11, 12, 13, 14, 15, 16}}
)

// connA1 builds a CONN/A1 PDU (the OUT channel opener): VERSION, the connection
// and channel cookies, and the receive window size.
func connA1() []byte {
	return ndr.Frame(ndr.PktRTS, ndr.PfcFirstFrag|ndr.PfcLastFrag, 0x10, buildRTSBody(rtsFlagNone, []rtsCommand{
		{Type: rtsVersion, U32: 1},
		{Type: rtsCookie, GUID: testConnCookie},
		{Type: rtsCookie, GUID: testChanCookie},
		{Type: rtsReceiveWindowSize, U32: 0x10000},
	}))
}

// connB1 builds a CONN/B1 PDU (the IN channel opener).
func connB1() []byte {
	return ndr.Frame(ndr.PktRTS, ndr.PfcFirstFrag|ndr.PfcLastFrag, 0x20, buildRTSBody(rtsFlagNone, []rtsCommand{
		{Type: rtsVersion, U32: 1},
		{Type: rtsCookie, GUID: testConnCookie},
		{Type: rtsCookie, GUID: testChanCookie},
		{Type: rtsChannelLifetime, U32: 0x40000000},
		{Type: rtsClientKeepalive, U32: 0},
		{Type: rtsAssociationGroupID, GUID: testConnCookie},
	}))
}

// TestParseProxyURL proves the rpcproxy query yields the proxied host and port,
// and rejects a query without them.
func TestParseProxyURL(t *testing.T) {
	r := httptest.NewRequest("RPC_OUT_DATA", "/rpc/rpcproxy.dll?mail.hermex.test:6001", nil)
	host, port, ok := parseProxyURL(r)
	if !ok || host != "mail.hermex.test" || port != "6001" {
		t.Errorf("parseProxyURL = (%q, %q, %v), want (mail.hermex.test, 6001, true)", host, port, ok)
	}
	for _, target := range []string{"/rpc/rpcproxy.dll", "/rpc/rpcproxy.dll?:6001", "/rpc/rpcproxy.dll?mail.hermex.test:"} {
		bad := httptest.NewRequest("RPC_OUT_DATA", target, nil)
		if _, _, ok := parseProxyURL(bad); ok {
			t.Errorf("parseProxyURL(%q) accepted malformed query", target)
		}
	}
}

// TestRTSRoundTrip proves the CONN/A3 and CONN/C2 builders emit RTS PDUs whose
// command lists decode back to the documented shapes.
func TestRTSRoundTrip(t *testing.T) {
	_, a3, err := parseRTS(buildConnA3(0x10))
	mustNoErr(t, err, "parse CONN/A3")
	wantCommands(t, a3, "CONN/A3", rtsConnectionTimeout)
	wantEq(t, a3[0].U32, connectionTimeoutMS, "CONN/A3 timeout")

	_, c2, err := parseRTS(buildConnC2(0x10, 0x10000))
	mustNoErr(t, err, "parse CONN/C2")
	wantCommands(t, c2, "CONN/C2", rtsVersion, rtsReceiveWindowSize, rtsConnectionTimeout)
	wantEq(t, c2[0].U32, uint32(1), "CONN/C2 version")
	wantEq(t, c2[1].U32, uint32(0x10000), "CONN/C2 window")
	wantEq(t, c2[2].U32, connectionTimeoutMS, "CONN/C2 timeout")

	// The cookies helper recovers both cookies from a CONN/A1 in order.
	_, a1cmds, err := parseRTS(connA1())
	mustNoErr(t, err, "parse CONN/A1")
	ck := cookies(a1cmds)
	if len(ck) != 2 {
		t.Fatalf("cookies(A1) = %v, want two", ck)
	}
	wantEq(t, ck[0], testConnCookie, "the connection cookie")
	wantEq(t, ck[1], testChanCookie, "the channel cookie")
}

// TestRendezvous proves the dual-channel handshake: an RPC_OUT_DATA request
// opens the OUT channel and gets CONN/A3 immediately; once an RPC_IN_DATA
// request opens the matching IN channel (same connection cookie), the server
// emits CONN/C2 on the OUT channel, the "virtual connection established" event
// that splices the two HTTP requests into one logical RPC connection.
func TestRendezvous(t *testing.T) {
	srv := httptest.NewServer(NewServer(Config{Auth: okAuth}))
	defer srv.Close()
	url := srv.URL + "/rpc/rpcproxy.dll?testhost:6001"

	// Open the OUT channel and read the immediate CONN/A3.
	outReq := mustReq(t, "RPC_OUT_DATA", url, bytes.NewReader(connA1()))
	outResp, err := http.DefaultClient.Do(outReq)
	mustNoErr(t, err, "OUT request")
	defer func() {
		if cerr := outResp.Body.Close(); cerr != nil {
			t.Errorf("close OUT body: %v", cerr)
		}
	}()
	wantEq(t, outResp.StatusCode, http.StatusOK, "OUT status")

	a3, err := readPDU(outResp.Body)
	mustNoErr(t, err, "read CONN/A3")
	_, a3cmds, err := parseRTS(a3)
	mustNoErr(t, err, "parse CONN/A3")
	wantCommands(t, a3cmds, "the first OUT PDU", rtsConnectionTimeout)

	// Open the IN channel via a pipe body so it stays open while we read the OUT
	// stream.
	pr, pw := io.Pipe()
	inReq := mustReq(t, "RPC_IN_DATA", url, pr)
	inDone := make(chan error, 1)
	go func() {
		resp, doErr := http.DefaultClient.Do(inReq)
		if resp != nil {
			if cerr := resp.Body.Close(); cerr != nil && doErr == nil {
				doErr = cerr
			}
		}
		inDone <- doErr
	}()
	_, err = pw.Write(connB1())
	mustNoErr(t, err, "write CONN/B1")

	// The virtual connection is now complete: CONN/C2 must arrive on OUT.
	c2, err := readPDU(outResp.Body)
	mustNoErr(t, err, "read CONN/C2")
	_, cmds, err := parseRTS(c2)
	mustNoErr(t, err, "parse CONN/C2")
	wantCommands(t, cmds, "the second OUT PDU", rtsVersion, rtsReceiveWindowSize, rtsConnectionTimeout)

	mustNoErr(t, pw.Close(), "close IN pipe")
	if err := <-inDone; err != nil {
		t.Errorf("RPC_IN_DATA request: %v", err)
	}
}

// TestUnknownMethodRejected proves a non-RPC verb is refused without creating state.
func TestUnknownMethodRejected(t *testing.T) {
	s := NewServer(Config{Auth: okAuth})
	srv := httptest.NewServer(s)
	defer srv.Close()
	resp, err := http.DefaultClient.Do(mustReq(t, "GET", srv.URL+"/rpc/rpcproxy.dll?h:6001", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close GET body: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", resp.StatusCode)
	}
	wantEq(t, resp.Header.Get("Allow"), "RPC_IN_DATA, RPC_OUT_DATA", "Allow header")
	s.mu.Lock()
	n := len(s.conns)
	s.mu.Unlock()
	wantEq(t, n, 0, "connections after rejected method")
}

// TestRTSAndPDURejectMalformed proves readPDU and parseRTS refuse truncated
// frames, frag_length below the 16-byte NCACN header, and unknown RTS commands.
func TestRTSAndPDURejectMalformed(t *testing.T) {
	// readPDU refuses a header declaring frag_length < 16 ([C706] 12.6.3.1).
	shortFrag := make([]byte, 16)
	shortFrag[0] = 5 // rpc_vers
	shortFrag[2] = ndr.PktRTS
	shortFrag[4] = 0x10                               // little-endian NDR data representation
	binary.LittleEndian.PutUint16(shortFrag[8:10], 8) // frag_length < 16
	_, err := readPDU(bytes.NewReader(shortFrag))
	wantTrue(t, errors.Is(err, errMalformedPDU), "frag_length < 16 returns errMalformedPDU")

	// parseRTS refuses a PDU shorter than the 16-byte header.
	_, _, err = parseRTS([]byte{5, 0, ndr.PktRTS})
	wantTrue(t, err != nil, "short RTS PDU returns error")

	// parseRTS refuses an unknown RTS command type ([MS-RPCH] 2.2.3.5.1).
	unknownCmd := ndr.Frame(ndr.PktRTS, ndr.PfcFirstFrag|ndr.PfcLastFrag, 1, buildRTSBody(rtsFlagNone, []rtsCommand{
		{Type: 99, U32: 0},
	}))
	_, _, err = parseRTS(unknownCmd)
	wantTrue(t, errors.Is(err, ndr.ErrFormat), "unknown RTS command returns ErrFormat")

	// parseRTS refuses a truncated padding command whose length exceeds the body.
	padBody := ndr.NewPush()
	padBody.Uint16(rtsFlagNone)
	padBody.Uint16(1)
	padBody.Uint32(rtsPadding)
	padBody.Uint32(64) // declares 64 bytes of padding with 0 following
	padPDU := ndr.Frame(ndr.PktRTS, ndr.PfcFirstFrag|ndr.PfcLastFrag, 2, padBody.Bytes())
	_, _, err = parseRTS(padPDU)
	wantTrue(t, err != nil, "truncated RTS padding returns error")
}

// TestTransportRefusesHostileRequests proves ServeHTTP refuses unauthenticated,
// malformed, cookie-less, and over-capacity requests without mutating s.conns.
func TestTransportRefusesHostileRequests(t *testing.T) {
	// Unauthenticated request returns 401 and leaves s.conns empty.
	denyAuth := func(w http.ResponseWriter, _ *http.Request) (string, string, bool) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", "", false
	}
	sDeny := NewServer(Config{Auth: denyAuth})
	rec := httptest.NewRecorder()
	sDeny.ServeHTTP(rec, httptest.NewRequest("RPC_OUT_DATA", "/rpc/rpcproxy.dll?h:6001", bytes.NewReader(connA1())))
	wantEq(t, rec.Code, http.StatusUnauthorized, "unauthenticated status")
	sDeny.mu.Lock()
	wantEq(t, len(sDeny.conns), 0, "unauthenticated conns count")
	sDeny.mu.Unlock()

	// Malformed requests with valid auth return 400 and leave s.conns empty.
	noCookie := ndr.Frame(ndr.PktRTS, ndr.PfcFirstFrag|ndr.PfcLastFrag, 1, buildRTSBody(rtsFlagNone, []rtsCommand{
		{Type: rtsVersion, U32: 1},
	}))
	unknownCmd := ndr.Frame(ndr.PktRTS, ndr.PfcFirstFrag|ndr.PfcLastFrag, 1, buildRTSBody(rtsFlagNone, []rtsCommand{
		{Type: 99, U32: 0},
	}))
	s := NewServer(Config{Auth: okAuth})
	badReqs := []struct {
		name   string
		method string
		target string
		body   []byte
	}{
		{"missing host:port", "RPC_OUT_DATA", "/rpc/rpcproxy.dll", connA1()},
		{"truncated body", "RPC_OUT_DATA", "/rpc/rpcproxy.dll?h:6001", []byte{5, 0}},
		{"unknown RTS cmd", "RPC_OUT_DATA", "/rpc/rpcproxy.dll?h:6001", unknownCmd},
		{"OUT missing cookie", "RPC_OUT_DATA", "/rpc/rpcproxy.dll?h:6001", noCookie},
		{"IN missing cookie", "RPC_IN_DATA", "/rpc/rpcproxy.dll?h:6001", noCookie},
	}
	for _, tc := range badReqs {
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest(tc.method, tc.target, bytes.NewReader(tc.body)))
		wantEq(t, r.Code, http.StatusBadRequest, tc.name+" status")
	}
	s.mu.Lock()
	wantEq(t, len(s.conns), 0, "conns count after 400 refusals")
	s.mu.Unlock()

	// Table at capacity returns 503 and does not admit a new connection.
	for i := range maxVirtualConnections {
		s.getOrCreate("cap-"+strconv.Itoa(i), "alice@hermex.test", "/mb/alice", "203.0.113.7:1")
	}
	capRec := httptest.NewRecorder()
	s.ServeHTTP(capRec, httptest.NewRequest("RPC_OUT_DATA", "/rpc/rpcproxy.dll?h:6001", bytes.NewReader(connA1())))
	wantEq(t, capRec.Code, http.StatusServiceUnavailable, "over-capacity status")
	s.mu.Lock()
	wantEq(t, len(s.conns), maxVirtualConnections, "conns count after 503 refusal")
	s.mu.Unlock()
}

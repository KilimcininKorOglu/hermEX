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
	bad := httptest.NewRequest("RPC_OUT_DATA", "/rpc/rpcproxy.dll", nil)
	if _, _, ok := parseProxyURL(bad); ok {
		t.Error("parseProxyURL accepted a query without host:port")
	}
}

// TestRTSRoundTrip proves the CONN/A3 and CONN/C2 builders emit RTS PDUs whose
// command lists decode back to the documented shapes.
func TestRTSRoundTrip(t *testing.T) {
	_, a3, err := parseRTS(buildConnA3(0x10))
	mustNoErr(t, err, "parse CONN/A3")
	wantCommands(t, a3, "CONN/A3", rtsConnectionTimeout)

	_, c2, err := parseRTS(buildConnC2(0x10, 0x10000))
	mustNoErr(t, err, "parse CONN/C2")
	wantCommands(t, c2, "CONN/C2", rtsVersion, rtsReceiveWindowSize, rtsConnectionTimeout)
	wantEq(t, c2[1].U32, uint32(0x10000), "CONN/C2 window")

	// The cookies helper recovers both cookies from a CONN/A1 in order.
	_, a1cmds, _ := parseRTS(connA1())
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
	outReq, _ := http.NewRequest("RPC_OUT_DATA", url, bytes.NewReader(connA1()))
	outResp, err := http.DefaultClient.Do(outReq)
	mustNoErr(t, err, "OUT request")
	defer outResp.Body.Close()
	wantEq(t, outResp.StatusCode, http.StatusOK, "OUT status")

	a3, err := readPDU(outResp.Body)
	mustNoErr(t, err, "read CONN/A3")
	_, a3cmds, _ := parseRTS(a3)
	wantCommands(t, a3cmds, "the first OUT PDU", rtsConnectionTimeout)

	// Open the IN channel via a pipe body so it stays open while we read the OUT
	// stream.
	pr, pw := io.Pipe()
	inReq, _ := http.NewRequest("RPC_IN_DATA", url, pr)
	inDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(inReq)
		if resp != nil {
			resp.Body.Close()
		}
		inDone <- err
	}()
	_, err = pw.Write(connB1())
	mustNoErr(t, err, "write CONN/B1")

	// The virtual connection is now complete: CONN/C2 must arrive on OUT.
	c2, err := readPDU(outResp.Body)
	mustNoErr(t, err, "read CONN/C2")
	_, cmds, _ := parseRTS(c2)
	wantCommands(t, cmds, "the second OUT PDU", rtsVersion, rtsReceiveWindowSize, rtsConnectionTimeout)

	_ = pw.Close()
	<-inDone
}

// TestUnknownMethodRejected proves a non-RPC verb is refused.
func TestUnknownMethodRejected(t *testing.T) {
	srv := httptest.NewServer(NewServer(Config{Auth: okAuth}))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/rpc/rpcproxy.dll?h:6001", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", resp.StatusCode)
	}
}

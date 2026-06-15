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
	if err != nil {
		t.Fatalf("parse A3: %v", err)
	}
	if len(a3) != 1 || a3[0].Type != rtsConnectionTimeout {
		t.Errorf("CONN/A3 commands = %v, want [CONNECTION_TIMEOUT]", a3)
	}
	_, c2, err := parseRTS(buildConnC2(0x10, 0x10000))
	if err != nil {
		t.Fatalf("parse C2: %v", err)
	}
	if len(c2) != 3 || c2[0].Type != rtsVersion || c2[1].Type != rtsReceiveWindowSize || c2[2].Type != rtsConnectionTimeout {
		t.Errorf("CONN/C2 commands = %v, want [VERSION, RECEIVE_WINDOW_SIZE, CONNECTION_TIMEOUT]", c2)
	}
	if c2[1].U32 != 0x10000 {
		t.Errorf("CONN/C2 window = %#x, want 0x10000", c2[1].U32)
	}
	// The cookies helper recovers both cookies from a CONN/A1 in order.
	_, a1cmds, _ := parseRTS(connA1())
	ck := cookies(a1cmds)
	if len(ck) != 2 || ck[0] != testConnCookie || ck[1] != testChanCookie {
		t.Errorf("cookies(A1) = %v, want [conn, chan]", ck)
	}
}

// TestRendezvous proves the dual-channel handshake: an RPC_OUT_DATA request
// opens the OUT channel and gets CONN/A3 immediately; once an RPC_IN_DATA
// request opens the matching IN channel (same connection cookie), the server
// emits CONN/C2 on the OUT channel — the "virtual connection established" event
// that splices the two HTTP requests into one logical RPC connection.
func TestRendezvous(t *testing.T) {
	srv := httptest.NewServer(NewServer(Config{Auth: okAuth}))
	defer srv.Close()
	url := srv.URL + "/rpc/rpcproxy.dll?testhost:6001"

	// Open the OUT channel and read the immediate CONN/A3.
	outReq, _ := http.NewRequest("RPC_OUT_DATA", url, bytes.NewReader(connA1()))
	outResp, err := http.DefaultClient.Do(outReq)
	if err != nil {
		t.Fatalf("OUT request: %v", err)
	}
	defer outResp.Body.Close()
	if outResp.StatusCode != http.StatusOK {
		t.Fatalf("OUT status = %d, want 200", outResp.StatusCode)
	}
	a3, err := readPDU(outResp.Body)
	if err != nil {
		t.Fatalf("read CONN/A3: %v", err)
	}
	if _, cmds, _ := parseRTS(a3); len(cmds) != 1 || cmds[0].Type != rtsConnectionTimeout {
		t.Fatalf("first OUT PDU is not CONN/A3: %v", cmds)
	}

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
	if _, err := pw.Write(connB1()); err != nil {
		t.Fatalf("write CONN/B1: %v", err)
	}

	// The virtual connection is now complete: CONN/C2 must arrive on OUT.
	c2, err := readPDU(outResp.Body)
	if err != nil {
		t.Fatalf("read CONN/C2: %v", err)
	}
	_, cmds, _ := parseRTS(c2)
	if len(cmds) != 3 || cmds[0].Type != rtsVersion || cmds[1].Type != rtsReceiveWindowSize || cmds[2].Type != rtsConnectionTimeout {
		t.Errorf("second OUT PDU is not CONN/C2: %v", cmds)
	}

	pw.Close()
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

package mapihttp

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/oxmapihttp"
)

const (
	testUser = "alice@hermex.test"
	testPass = "test1234"
)

// newTestServer builds a MAPI/HTTP server over a single static account.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// mapiPost issues a MAPI/HTTP request. headers controls X-RequestId/X-ClientInfo
// presence; auth toggles Basic credentials; cookies are forwarded.
func mapiPost(t *testing.T, ts *httptest.Server, path, reqType string, body []byte, opts func(*http.Request)) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(testUser, testPass)
	req.Header.Set("X-RequestType", reqType)
	req.Header.Set("X-RequestId", "req-1")
	req.Header.Set("X-ClientInfo", "client-1")
	if opts != nil {
		opts(req)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// connectBody is a minimal well-formed Connect request body.
func connectBody() []byte {
	b := []byte{0}                        // UserDn (empty NUL-terminated ASCII)
	return append(b, make([]byte, 16)...) // Flags, DefaultCodePage, LcidString, LcidSort
}

// executeBody is a minimal Execute request body with an empty ROP buffer.
func executeBody() []byte {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, 0) // Flags
	b = binary.LittleEndian.AppendUint32(b, 0) // RopBufferSize
	b = binary.LittleEndian.AppendUint32(b, 0) // MaxRopOut
	return b
}

func cookieByName(resp *http.Response, name string) string {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// TestAuthRequired confirms the EMSMDB endpoint rejects an unauthenticated request.
func TestAuthRequired(t *testing.T) {
	ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mapi/emsmdb", nil)
	req.Header.Set("X-RequestType", "PING")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// TestMissingHeader confirms a request without X-RequestId fails with the
// missing-header response code.
func TestMissingHeader(t *testing.T) {
	ts := newTestServer(t)
	resp := mapiPost(t, ts, "/mapi/emsmdb", "PING", nil, func(r *http.Request) {
		r.Header.Del("X-RequestId")
	})
	defer resp.Body.Close()
	if got := resp.Header.Get("X-ResponseCode"); got != "3" {
		t.Errorf("X-ResponseCode = %q, want 3 (missing header)", got)
	}
}

// TestInvalidRequestType confirms an unknown X-RequestType is rejected.
func TestInvalidRequestType(t *testing.T) {
	ts := newTestServer(t)
	resp := mapiPost(t, ts, "/mapi/emsmdb", "Bogus", nil, nil)
	defer resp.Body.Close()
	if got := resp.Header.Get("X-ResponseCode"); got != "8" {
		t.Errorf("X-ResponseCode = %q, want 8 (invalid request type)", got)
	}
}

// TestConnectFraming confirms Connect succeeds, sets the sid + sequence cookies,
// and frames the chunked PROCESSING/DONE body.
func TestConnectFraming(t *testing.T) {
	ts := newTestServer(t)
	resp := mapiPost(t, ts, "/mapi/emsmdb", "Connect", connectBody(), nil)
	defer resp.Body.Close()
	if got := resp.Header.Get("X-ResponseCode"); got != "0" {
		t.Errorf("X-ResponseCode = %q, want 0", got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/mapi-http" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cookieByName(resp, "sid") == "" || cookieByName(resp, "sequence") == "" {
		t.Errorf("Connect did not set sid + sequence cookies")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "PROCESSING\r\nDONE\r\n") {
		t.Errorf("body missing PROCESSING/DONE preamble: %q", body[:min(40, len(body))])
	}
}

// TestExecuteFlow confirms the cookie lifecycle: Execute needs the cookies,
// succeeds with them, and rolls the sequence; a bad sid is rejected.
func TestExecuteFlow(t *testing.T) {
	ts := newTestServer(t)
	conn := mapiPost(t, ts, "/mapi/emsmdb", "Connect", connectBody(), nil)
	conn.Body.Close()
	sid, seq := cookieByName(conn, "sid"), cookieByName(conn, "sequence")
	if sid == "" || seq == "" {
		t.Fatal("no cookies from Connect")
	}

	// Execute without cookies -> missing cookie (6).
	noCookie := mapiPost(t, ts, "/mapi/emsmdb", "Execute", executeBody(), nil)
	noCookie.Body.Close()
	if got := noCookie.Header.Get("X-ResponseCode"); got != "6" {
		t.Errorf("Execute without cookies: X-ResponseCode = %q, want 6", got)
	}

	// Execute with valid cookies -> success, rolled sequence.
	ok := mapiPost(t, ts, "/mapi/emsmdb", "Execute", executeBody(), func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	ok.Body.Close()
	if got := ok.Header.Get("X-ResponseCode"); got != "0" {
		t.Errorf("Execute with cookies: X-ResponseCode = %q, want 0", got)
	}
	if newSeq := cookieByName(ok, "sequence"); newSeq == "" || newSeq == seq {
		t.Errorf("Execute did not roll the sequence cookie (was %q, got %q)", seq, newSeq)
	}

	// Execute with a bogus sid -> invalid context cookie (2).
	bad := mapiPost(t, ts, "/mapi/emsmdb", "Execute", executeBody(), func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: "nope"})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	bad.Body.Close()
	if got := bad.Header.Get("X-ResponseCode"); got != "2" {
		t.Errorf("Execute with bad sid: X-ResponseCode = %q, want 2", got)
	}
}

// TestDisconnect confirms Disconnect drops the session and that the dropped
// session no longer validates on Execute.
func TestDisconnect(t *testing.T) {
	ts := newTestServer(t)
	conn := mapiPost(t, ts, "/mapi/emsmdb", "Connect", connectBody(), nil)
	conn.Body.Close()
	sid, seq := cookieByName(conn, "sid"), cookieByName(conn, "sequence")

	disc := mapiPost(t, ts, "/mapi/emsmdb", "Disconnect", nil, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
	})
	disc.Body.Close()
	if got := disc.Header.Get("X-ResponseCode"); got != "0" {
		t.Errorf("Disconnect: X-ResponseCode = %q, want 0", got)
	}

	after := mapiPost(t, ts, "/mapi/emsmdb", "Execute", executeBody(), func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	after.Body.Close()
	if got := after.Header.Get("X-ResponseCode"); got != "2" {
		t.Errorf("Execute after Disconnect: X-ResponseCode = %q, want 2 (invalid context)", got)
	}
}

// TestNspiRouted confirms the NSPI endpoint is routed and authenticated (it
// answers PING; the address-book calls land in a later sub-slice).
func TestNspiRouted(t *testing.T) {
	ts := newTestServer(t)
	resp := mapiPost(t, ts, "/mapi/nspi", "PING", nil, nil)
	defer resp.Body.Close()
	if got := resp.Header.Get("X-ResponseCode"); got != "0" {
		t.Errorf("NSPI PING: X-ResponseCode = %q, want 0", got)
	}
}

// TestExecuteRopFraming confirms the Execute response carries a valid, decodable
// RPC_HEADER_EXT ROP buffer (the transport <-> oxmapihttp codec wiring). A
// truncated ROP (a bare RopId) cannot complete its header, so dispatch yields an
// empty but valid buffer; TestExecuteRopLogon drives a full RopLogon end to end.
func TestExecuteRopFraming(t *testing.T) {
	ts := newTestServer(t)
	conn := mapiPost(t, ts, "/mapi/emsmdb", "Connect", connectBody(), nil)
	conn.Body.Close()
	sid, seq := cookieByName(conn, "sid"), cookieByName(conn, "sequence")

	// Execute carrying a truncated ROP (RopId only): dispatch can't read the
	// rest of the header, so the response is an empty buffer.
	reqRop := oxmapihttp.EncodeExecute([]byte{0xFE}, nil)
	var eb []byte
	eb = binary.LittleEndian.AppendUint32(eb, 0)                   // Flags
	eb = binary.LittleEndian.AppendUint32(eb, uint32(len(reqRop))) // RopBufferSize
	eb = append(eb, reqRop...)                                     // RopBuffer
	eb = binary.LittleEndian.AppendUint32(eb, 0x10000)             // MaxRopOut

	resp := mapiPost(t, ts, "/mapi/emsmdb", "Execute", eb, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// strip the meta preamble; payload = execute_response: Status|Error|Flags|RopBufferSize|RopBuffer|AuxBufSize
	_, payload, found := bytes.Cut(body, []byte("\r\n\r\n"))
	if !found {
		t.Fatal("response missing meta preamble terminator")
	}
	if len(payload) < 16 {
		t.Fatalf("execute response too short: %d bytes", len(payload))
	}
	cbOut := binary.LittleEndian.Uint32(payload[12:])
	if int(16+cbOut) > len(payload) {
		t.Fatalf("RopBufferSize %d overruns payload %d", cbOut, len(payload))
	}
	rops, handles, err := oxmapihttp.DecodeExecute(payload[16 : 16+cbOut])
	if err != nil {
		t.Fatalf("Execute response RopBuffer did not decode: %v", err)
	}
	if len(rops) != 0 || len(handles) != 0 {
		t.Errorf("truncated ROP should frame an empty buffer, got rops=%x handles=%v", rops, handles)
	}
}

// TestExecuteRopLogon drives a complete RopLogon through the full stack —
// Connect, then Execute with a real ROP buffer — and confirms the decoded
// response carries a non-null output handle plus the 13 special-folder EIDs.
// This exercises the DecodeExecute -> Dispatch -> EncodeExecute path that
// cross-Execute handle persistence rides on.
func TestExecuteRopLogon(t *testing.T) {
	ts := newTestServer(t)
	conn := mapiPost(t, ts, "/mapi/emsmdb", "Connect", connectBody(), nil)
	conn.Body.Close()
	sid, seq := cookieByName(conn, "sid"), cookieByName(conn, "sequence")

	// RopLogon request: header (RopId, LogonId, OutputHandleIndex) + private
	// LOGON_REQUEST body (LogonFlags, OpenFlags, StoreState, EssdnSize).
	ropReq := []byte{0xFE, 0x00, 0x00, 0x01}             // Logon, LogonId, hindex, LogonFlags=Private
	ropReq = binary.LittleEndian.AppendUint32(ropReq, 0) // OpenFlags
	ropReq = binary.LittleEndian.AppendUint32(ropReq, 0) // StoreState
	ropReq = binary.LittleEndian.AppendUint16(ropReq, 0) // EssdnSize
	reqRop := oxmapihttp.EncodeExecute(ropReq, []uint32{0xFFFFFFFF})

	var eb []byte
	eb = binary.LittleEndian.AppendUint32(eb, 0)                   // Flags
	eb = binary.LittleEndian.AppendUint32(eb, uint32(len(reqRop))) // RopBufferSize
	eb = append(eb, reqRop...)                                     // RopBuffer
	eb = binary.LittleEndian.AppendUint32(eb, 0x10000)             // MaxRopOut

	resp := mapiPost(t, ts, "/mapi/emsmdb", "Execute", eb, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, payload, found := bytes.Cut(body, []byte("\r\n\r\n"))
	if !found || len(payload) < 16 {
		t.Fatalf("malformed execute response (%d bytes)", len(payload))
	}
	cbOut := binary.LittleEndian.Uint32(payload[12:])
	if int(16+cbOut) > len(payload) {
		t.Fatalf("RopBufferSize %d overruns payload %d", cbOut, len(payload))
	}
	rops, handles, err := oxmapihttp.DecodeExecute(payload[16 : 16+cbOut])
	if err != nil {
		t.Fatalf("decode logon response: %v", err)
	}
	if len(handles) != 1 || handles[0] == 0xFFFFFFFF {
		t.Fatalf("logon did not set the output handle: %v", handles)
	}
	// Response: RopId(0xFE), OutputHandleIndex(0), ReturnValue(0), LogonFlags,
	// then 13 FolderId EIDs. Confirm the header and that all 13 EIDs fit.
	if len(rops) < 3+1+13*8 {
		t.Fatalf("logon response too short: %d bytes", len(rops))
	}
	if rops[0] != 0xFE || rops[1] != 0x00 {
		t.Errorf("response header = % x, want FE 00", rops[0:2])
	}
	if ec := binary.LittleEndian.Uint32(rops[2:]); ec != 0 {
		t.Errorf("ReturnValue = %#x, want 0", ec)
	}
}

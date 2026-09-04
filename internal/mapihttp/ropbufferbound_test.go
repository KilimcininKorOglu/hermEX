package mapihttp

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxmapihttp"
)

// seedLargeMessage appends one message whose body is big enough that a single
// FastTransfer GetBuffer at the maximum buffer size returns more than the 32 KiB
// the transport is willing to frame.
func seedLargeMessage(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := []byte("From: bob@hermex.test\r\nTo: " + testUser + "\r\nSubject: BIG\r\n" +
		"Date: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\n" + strings.Repeat("x", 200*1024) + "\r\n")
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
}

// executeRaw issues one Execute and returns the response's StatusCode, ErrorCode
// and RopBufferSize without asserting success, so a refusal can be inspected.
func executeRaw(t *testing.T, ts *httptest.Server, sid string, seq *string, ropBytes []byte, handles []uint32) (status, errCode, cbOut uint32) {
	t.Helper()
	reqRop := oxmapihttp.EncodeExecute(ropBytes, handles)
	var eb []byte
	eb = binary.LittleEndian.AppendUint32(eb, 0)
	eb = binary.LittleEndian.AppendUint32(eb, uint32(len(reqRop)))
	eb = append(eb, reqRop...)
	eb = binary.LittleEndian.AppendUint32(eb, 0x10000)

	resp := mapiPost(t, ts, "/mapi/emsmdb", "Execute", eb, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: *seq})
	})
	defer resp.Body.Close()
	if ns := cookieByName(resp, "sequence"); ns != "" {
		*seq = ns
	}
	body, _ := io.ReadAll(resp.Body)
	_, payload, found := bytes.Cut(body, []byte("\r\n\r\n"))
	if !found || len(payload) < 16 {
		t.Fatalf("malformed execute response (%d bytes)", len(payload))
	}
	return binary.LittleEndian.Uint32(payload[0:]),
		binary.LittleEndian.Uint32(payload[4:]),
		binary.LittleEndian.Uint32(payload[12:])
}

// TestExecuteRefusesUnframeableRopResponse is the ROP response bound. The buffer's
// RopSize and the RPC_HEADER_EXT SizeActual are 16-bit fields and nothing on this
// transport bounded what Dispatch returned, so a large response was framed with a
// size the fields could not carry once it passed 0xFFFF. The RPC/HTTP transport
// already refused anything over 32 KiB with ecResponseTooBig; this one framed it.
func TestExecuteRefusesUnframeableRopResponse(t *testing.T) {
	dir := t.TempDir()
	seedLargeMessage(t, dir)

	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test", nil).Handler())
	defer ts.Close()

	conn := mapiPost(t, ts, "/mapi/emsmdb", "Connect", connectBody(), nil)
	conn.Body.Close()
	sid, seq := cookieByName(conn, "sid"), cookieByName(conn, "sequence")
	if sid == "" || seq == "" {
		t.Fatal("no cookies from Connect")
	}

	_, h := execRops(t, ts, sid, &seq, ropLogonReq(0), []uint32{0xFFFFFFFF})
	logonH := h[0]

	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = execRops(t, ts, sid, &seq, ropOpenFolderReq(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]

	cfg := ropSyncConfigureReq(1, 2, objectstore.SyncTypeContents, objectstore.SyncNormal, nil)
	_, h = execRops(t, ts, sid, &seq, cfg, []uint32{logonH, folderH, 0xFFFFFFFF})
	syncH := h[2]

	// One GetBuffer at the largest buffer the field can name. The stream is far
	// bigger than that, so the response runs well past the 32 KiB bound.
	status, errCode, cbOut := executeRaw(t, ts, sid, &seq, ropGetBufferReq(0, 0xFFFF), []uint32{syncH})
	if status != rcSuccess {
		t.Fatalf("StatusCode = %d, want %d", status, rcSuccess)
	}
	if errCode != ecResponseTooBig {
		t.Fatalf("ErrorCode = %#x with RopBufferSize %d, want %#x (the response was framed anyway)",
			errCode, cbOut, ecResponseTooBig)
	}
	if cbOut != 0 {
		t.Errorf("RopBufferSize = %d on a refused Execute, want 0", cbOut)
	}
}

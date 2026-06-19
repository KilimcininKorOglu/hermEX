package mapihttp

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxmapihttp"
)

// seedSyncMessage delivers one message into the account's Inbox so the ICS
// contents download has something to stream. It opens the same store the server
// reopens on logon, so the message is already present when the transport drives
// the sync.
func seedSyncMessage(t *testing.T, dir, subject string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := []byte("From: bob@hermex.test\r\nTo: " + testUser + "\r\nSubject: " + subject +
		"\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n\r\nbody\r\n")
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
}

// execRops issues one Execute carrying ropBytes + the server-handle table,
// confirms it framed successfully, rolls the sequence cookie forward, and returns
// the decoded response ROP buffer and handle table.
func execRops(t *testing.T, ts *httptest.Server, sid string, seq *string, ropBytes []byte, handles []uint32) ([]byte, []uint32) {
	t.Helper()
	reqRop := oxmapihttp.EncodeExecute(ropBytes, handles)
	var eb []byte
	eb = binary.LittleEndian.AppendUint32(eb, 0)                   // Flags
	eb = binary.LittleEndian.AppendUint32(eb, uint32(len(reqRop))) // RopBufferSize
	eb = append(eb, reqRop...)                                     // RopBuffer
	eb = binary.LittleEndian.AppendUint32(eb, 0x10000)             // MaxRopOut

	resp := mapiPost(t, ts, "/mapi/emsmdb", "Execute", eb, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: *seq})
	})
	defer resp.Body.Close()
	if got := resp.Header.Get("X-ResponseCode"); got != "0" {
		t.Fatalf("Execute X-ResponseCode = %q, want 0", got)
	}
	if ns := cookieByName(resp, "sequence"); ns != "" {
		*seq = ns
	}
	body, _ := io.ReadAll(resp.Body)
	_, payload, found := bytes.Cut(body, []byte("\r\n\r\n"))
	if !found || len(payload) < 16 {
		t.Fatalf("malformed execute response (%d bytes)", len(payload))
	}
	cbOut := binary.LittleEndian.Uint32(payload[12:])
	if int(16+cbOut) > len(payload) {
		t.Fatalf("RopBufferSize %d overruns payload %d", cbOut, len(payload))
	}
	rops, hs, err := oxmapihttp.DecodeExecute(payload[16 : 16+cbOut])
	if err != nil {
		t.Fatalf("decode execute response: %v", err)
	}
	return rops, hs
}

// ropLogonReq builds a private-mailbox RopLogon placing the logon handle in slot
// hindex.
func ropLogonReq(hindex uint8) []byte {
	b := []byte{0xFE, 0x00, hindex, 0x01}      // Logon, LogonId, hindex, LogonFlags=Private
	b = binary.LittleEndian.AppendUint32(b, 0) // OpenFlags
	b = binary.LittleEndian.AppendUint32(b, 0) // StoreState
	b = binary.LittleEndian.AppendUint16(b, 0) // EssdnSize
	return b
}

// ropOpenFolderReq builds a RopOpenFolder reading the parent in slot inIdx and
// placing the opened folder in slot outIdx.
func ropOpenFolderReq(inIdx, outIdx uint8, folderEID uint64) []byte {
	b := []byte{0x02, 0x00, inIdx, outIdx}
	b = binary.LittleEndian.AppendUint64(b, folderEID)
	return append(b, 0x00) // OpenModeFlags
}

// ropSyncConfigureReq builds a RopSynchronizationConfigure on the folder in slot
// inIdx, placing the sync context in slot outIdx. propTags is the property filter
// (nil keeps everything).
func ropSyncConfigureReq(inIdx, outIdx, syncType uint8, syncFlags uint16, propTags []uint32) []byte {
	b := []byte{0x70, 0x00, inIdx, outIdx, syncType, 0x00} // ..., ohindex, syncType, SendOptions
	b = binary.LittleEndian.AppendUint16(b, syncFlags)
	b = binary.LittleEndian.AppendUint16(b, 0) // RestrictionSize
	b = binary.LittleEndian.AppendUint32(b, 0) // ExtraFlags
	b = binary.LittleEndian.AppendUint16(b, uint16(len(propTags)))
	for _, tag := range propTags {
		b = binary.LittleEndian.AppendUint32(b, tag)
	}
	return b
}

// ropGetBufferReq builds a RopFastTransferSourceGetBuffer draining the source in
// slot inIdx.
func ropGetBufferReq(inIdx uint8, bufferSize uint16) []byte {
	b := []byte{0x4E, 0x00, inIdx}
	return binary.LittleEndian.AppendUint16(b, bufferSize)
}

// parseGetBuffer decodes a single GetBuffer response: transfer_status and the
// length-prefixed transfer_data chunk.
func parseGetBuffer(t *testing.T, rops []byte) (status uint16, chunk []byte) {
	t.Helper()
	if len(rops) < 15 {
		t.Fatalf("GetBuffer response too short: %d bytes", len(rops))
	}
	if rops[0] != 0x4E {
		t.Fatalf("response RopId = %#x, want GetBuffer (0x4E)", rops[0])
	}
	if ec := binary.LittleEndian.Uint32(rops[2:]); ec != 0 {
		t.Fatalf("GetBuffer ReturnValue = %#x", ec)
	}
	status = binary.LittleEndian.Uint16(rops[6:]) // skip InProgress(2)+TotalStep(2)+Reserved(1)
	n := binary.LittleEndian.Uint16(rops[13:])
	if int(15+n) > len(rops) {
		t.Fatalf("transfer_data length %d overruns response %d", n, len(rops))
	}
	return status, rops[15 : 15+n]
}

// utf16le encodes an ASCII string the way the FastTransfer stream codec emits a
// PT_UNICODE value, so a downloaded subject can be located in the raw stream.
func utf16le(s string) []byte {
	b := make([]byte, 0, len(s)*2)
	for _, r := range s {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}

// TestExecuteRopSyncDownload drives the MS-OXCFXICS contents-download path over
// the full MAPI/HTTP transport: Connect, then Logon -> OpenFolder(Inbox) ->
// SyncConfigure -> a FastTransferSourceGetBuffer drain loop, each carried in its
// own Execute. It proves the ICS ROP buffer survives the RPC_HEADER_EXT + Execute
// framing and that cross-Execute handle persistence carries the sync context from
// the SyncConfigure Execute into every GetBuffer Execute. Both ends are hermex, so
// this is a self-driven smoke (dispatch + framing), NOT an independent wire oracle;
// the real-client cached-mode close stays Outlook-pending.
func TestExecuteRopSyncDownload(t *testing.T) {
	dir := t.TempDir()
	seedSyncMessage(t, dir, "SYNCME")

	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test", nil).Handler())
	defer ts.Close()

	conn := mapiPost(t, ts, "/mapi/emsmdb", "Connect", connectBody(), nil)
	conn.Body.Close()
	sid, seq := cookieByName(conn, "sid"), cookieByName(conn, "sequence")
	if sid == "" || seq == "" {
		t.Fatal("no cookies from Connect")
	}

	// Logon -> slot 0 = logon handle.
	_, h := execRops(t, ts, sid, &seq, ropLogonReq(0), []uint32{0xFFFFFFFF})
	logonH := h[0]
	if logonH == 0xFFFFFFFF {
		t.Fatal("Logon did not return a handle over the transport")
	}

	// OpenFolder(Inbox) -> slot 1 = folder handle.
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = execRops(t, ts, sid, &seq, ropOpenFolderReq(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]
	if folderH == 0xFFFFFFFF {
		t.Fatal("OpenFolder did not return a handle over the transport")
	}

	// SyncConfigure(contents, no property filter) -> slot 2 = sync-context handle.
	cfg := ropSyncConfigureReq(1, 2, objectstore.SyncTypeContents, objectstore.SyncNormal, nil)
	_, h = execRops(t, ts, sid, &seq, cfg, []uint32{logonH, folderH, 0xFFFFFFFF})
	syncH := h[2]
	if syncH == 0xFFFFFFFF {
		t.Fatal("SyncConfigure did not return a sync-context handle over the transport")
	}

	// Drain the FastTransfer stream across separate Execute calls. The small
	// buffer forces several chunks, so each GetBuffer Execute must re-resolve the
	// sync handle the SyncConfigure Execute created. Stop at transfer_status DONE.
	var stream []byte
	done := false
	for i := 0; i < 100 && !done; i++ {
		rops, _ := execRops(t, ts, sid, &seq, ropGetBufferReq(0, 256), []uint32{syncH})
		status, chunk := parseGetBuffer(t, rops)
		stream = append(stream, chunk...)
		switch status {
		case 0x0003: // DONE
			done = true
		case 0x0001: // PARTIAL — keep draining
		default:
			t.Fatalf("GetBuffer transfer_status = %#x (error or unexpected)", status)
		}
	}
	if !done {
		t.Fatal("FastTransfer stream never reached DONE within 100 GetBuffer calls")
	}
	if len(stream) == 0 {
		t.Fatal("FastTransfer stream was empty")
	}
	// The seeded message's subject rode the stream as UTF-16LE, proving the message
	// content — not just the framing — survived the transport.
	if !bytes.Contains(stream, utf16le("SYNCME")) {
		t.Errorf("downloaded stream (%d bytes) did not carry the seeded subject", len(stream))
	}
}

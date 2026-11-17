package activesync

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"hermex/internal/wbxml"
)

// estimateReq builds a GetItemEstimate request for the Inbox at the given key.
func estimateReq(key string) *wbxml.Node {
	return wbxml.Elem(wbxml.GIEGetItemEstimate,
		wbxml.Elem(wbxml.GIECollections,
			wbxml.Elem(wbxml.GIECollection,
				wbxml.Str(wbxml.ASSyncKey, key),
				wbxml.Str(wbxml.ASCollectionID, inboxID()))))
}

// pingReq builds a Ping request watching the Inbox with the given heartbeat.
func pingReq(heartbeat string) *wbxml.Node {
	return wbxml.Elem(wbxml.PGPing,
		wbxml.Str(wbxml.PGHeartbeatInt, heartbeat),
		wbxml.Elem(wbxml.PGFolders,
			wbxml.Elem(wbxml.PGFolder,
				wbxml.Str(wbxml.PGID, inboxID()),
				wbxml.Str(wbxml.PGClass, "Email"))))
}

// TestGetItemEstimate confirms the estimate equals the count of unsynced changes.
func TestGetItemEstimate(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 3)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", "")) // snapshot now holds 3, key 2
	seedInbox(t, dir, 2)                         // two new messages since the sync

	_, root := postCommand(t, ts, "GetItemEstimate", estimateReq("2"))
	resp := root.Child(wbxml.GIEResponse)
	if resp.ChildText(wbxml.GIEStatus) != "1" {
		t.Fatalf("status = %q, want 1", resp.ChildText(wbxml.GIEStatus))
	}
	if est := resp.Child(wbxml.GIECollection).ChildText(wbxml.GIEEstimate); est != "2" {
		t.Errorf("estimate = %q, want 2", est)
	}
}

// TestGetItemEstimateNotPrimed confirms an unsynced collection reports Status 2.
func TestGetItemEstimateNotPrimed(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)

	_, root := postCommand(t, ts, "GetItemEstimate", estimateReq("5"))
	resp := root.Child(wbxml.GIEResponse)
	if resp.ChildText(wbxml.GIEStatus) != "2" {
		t.Errorf("status = %q, want 2 (not primed)", resp.ChildText(wbxml.GIEStatus))
	}
}

// TestPingHeartbeatOutOfRange confirms an out-of-range heartbeat reports Status 5
// with the nearest acceptable interval, rather than silently holding for a
// different interval than the device asked for.
func TestPingHeartbeatOutOfRange(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "Ping", pingReq("99999"))
	if s := root.ChildText(wbxml.PGStatus); s != "5" {
		t.Errorf("status = %q, want 5 (heartbeat out of range)", s)
	}
	if hb := root.ChildText(wbxml.PGHeartbeatInt); hb != "3540" {
		t.Errorf("HeartbeatInterval = %q, want 3540 (the upper bound)", hb)
	}
}

// TestPingRefusesAMalformedHeartbeat proves the MS-ASCMD Status (Ping) table: a
// heartbeat that is too short or too long is answered Status 5 with the shortest
// or longest allowed interval, and one that is not a number is a syntax error,
// Status 4. The watched folder is unsynced, so an accepted heartbeat answers
// Status 7 at once instead of holding the request.
func TestPingRefusesAMalformedHeartbeat(t *testing.T) {
	ts, _ := seededServer(t)
	status := func(hb string) (string, string) {
		_, root := postCommand(t, ts, "Ping", pingReq(hb))
		return root.ChildText(wbxml.PGStatus), root.ChildText(wbxml.PGHeartbeatInt)
	}
	s, shortest := status("0")
	wantEq(t, "the status for a zero heartbeat", s, "5")
	for _, hb := range []string{"-5", "-99999999999999999999"} {
		s, got := status(hb)
		wantEq(t, "the status for heartbeat "+hb, s, "5")
		wantEq(t, "the bound for heartbeat "+hb, got, shortest)
	}
	s, longest := status("99999")
	wantEq(t, "the status for a long heartbeat", s, "5")
	// 18446744075 seconds wraps a nanosecond Duration to about 1.3s.
	for _, hb := range []string{"18446744075", "99999999999999999999"} {
		s, got := status(hb)
		wantEq(t, "the status for heartbeat "+hb, s, "5")
		wantEq(t, "the bound for heartbeat "+hb, got, longest)
	}
	lo, _ := strconv.Atoi(shortest)
	hi, _ := strconv.Atoi(longest)
	if lo <= 0 || lo >= hi {
		t.Errorf("shortest %q and longest %q are not an allowed range", shortest, longest)
	}
	for _, hb := range []string{shortest, longest} {
		s, _ := status(hb)
		wantEq(t, "the status for the returned bound "+hb, s, "7")
	}
	for _, hb := range []string{"abc", "1.5", " 30"} {
		s, _ := status(hb)
		wantEq(t, "the status for heartbeat "+strconv.Quote(hb), s, "4")
	}
}

// postEmptyPing sends a Ping with no body, which MS-ASCMD 3.1.5.8 allows once
// the device has sent its heartbeat and folder list.
func postEmptyPing(t *testing.T, ts *httptest.Server) *wbxml.Node {
	t.Helper()
	url := ts.URL + "/Microsoft-Server-ActiveSync?Cmd=Ping&User=" + testUser + "&DeviceId=dev1&DeviceType=iPhone"
	req, err := http.NewRequest("POST", url, nil)
	mustNoErr(t, "build the request", err)
	req.SetBasicAuth(testUser, testPass)
	req.Header.Set("Content-Type", "application/vnd.ms-sync.wbxml")
	resp, err := http.DefaultClient.Do(req)
	mustNoErr(t, "send the empty Ping", err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty Ping status %d: %s", resp.StatusCode, out)
	}
	root, err := wbxml.Unmarshal(out)
	mustNoErr(t, "decode the empty Ping reply", err)
	return root
}

// TestPingUsesTheCachedParameters proves MS-ASCMD 3.1.5.8: the server MUST cache
// the HeartbeatInterval and the Folders of a Ping and use them when a later Ping
// omits either, and an empty Ping reuses both. A device that never sent them is
// answered Status 3, "the Ping command request omitted required parameters".
func TestPingUsesTheCachedParameters(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)

	wantEq(t, "an empty Ping from a new device", postEmptyPing(t, ts).ChildText(wbxml.PGStatus), "3")
	foldersOnly := wbxml.Elem(wbxml.PGPing, wbxml.Elem(wbxml.PGFolders,
		wbxml.Elem(wbxml.PGFolder, wbxml.Str(wbxml.PGID, inboxID()), wbxml.Str(wbxml.PGClass, "Email"))))
	_, root := postCommand(t, ts, "Ping", foldersOnly)
	wantEq(t, "a Ping with no heartbeat ever sent", root.ChildText(wbxml.PGStatus), "3")

	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))
	_, root = postCommand(t, ts, "Ping", pingReq("1"))
	wantEq(t, "the full Ping", root.ChildText(wbxml.PGStatus), "1")

	// Heartbeat only: the cached Inbox is watched, so the expiry is Status 1.
	_, root = postCommand(t, ts, "Ping", wbxml.Elem(wbxml.PGPing, wbxml.Str(wbxml.PGHeartbeatInt, "1")))
	wantEq(t, "a Ping with only a heartbeat", root.ChildText(wbxml.PGStatus), "1")

	// Empty: the cached Inbox and heartbeat are used, and new mail is reported.
	seedInbox(t, dir, 1)
	root = postEmptyPing(t, ts)
	wantEq(t, "an empty Ping after new mail", root.ChildText(wbxml.PGStatus), "2")
	if f := root.Child(wbxml.PGFolders); f == nil || f.ChildText(wbxml.PGFolder) != inboxID() {
		t.Error("the empty Ping did not report the cached Inbox")
	}
}

// TestPingNoFolders confirms a Ping naming no folder to watch reports Status 3.
func TestPingNoFolders(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "Ping", wbxml.Elem(wbxml.PGPing, wbxml.Str(wbxml.PGHeartbeatInt, "30")))
	if s := root.ChildText(wbxml.PGStatus); s != "3" {
		t.Errorf("status = %q, want 3 (no pingable folders)", s)
	}
}

// TestPingFolderNotSynced confirms watching a folder the device has not synced
// reports Status 7 (the device must sync it first) rather than silently ignoring
// it and holding the heartbeat.
func TestPingFolderNotSynced(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)

	_, root := postCommand(t, ts, "Ping", pingReq("30"))
	if s := root.ChildText(wbxml.PGStatus); s != "7" {
		t.Errorf("status = %q, want 7 (folder needs sync)", s)
	}
}

// TestPingNoChange confirms the heartbeat expires with Status 1 when nothing
// changes.
func TestPingNoChange(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", "")) // snapshot synced

	_, root := postCommand(t, ts, "Ping", pingReq("1"))
	if root.ChildText(wbxml.PGStatus) != "1" {
		t.Errorf("status = %q, want 1 (heartbeat expired)", root.ChildText(wbxml.PGStatus))
	}
}

// TestPingDetectsChange confirms a new message makes Ping return Status 2 with
// the changed folder, without waiting out the heartbeat.
func TestPingDetectsChange(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))
	seedInbox(t, dir, 1) // a new message arrives after the sync

	_, root := postCommand(t, ts, "Ping", pingReq("30"))
	if root.ChildText(wbxml.PGStatus) != "2" {
		t.Fatalf("status = %q, want 2 (changes)", root.ChildText(wbxml.PGStatus))
	}
	folders := root.Child(wbxml.PGFolders)
	if folders == nil || folders.ChildText(wbxml.PGFolder) != inboxID() {
		t.Errorf("Ping did not report the Inbox as changed")
	}
}

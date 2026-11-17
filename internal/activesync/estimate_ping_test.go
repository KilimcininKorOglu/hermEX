package activesync

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
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
	// The operator's floor: 60 seconds, the Sync heartbeat minimum.
	wantEq(t, "the shortest heartbeat", shortest, "60")
	for _, hb := range []string{"1", "59"} {
		s, got := status(hb)
		wantEq(t, "the status for heartbeat "+hb, s, "5")
		wantEq(t, "the bound for heartbeat "+hb, got, "60")
	}
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
	_, root = postCommand(t, ts, "Ping", pingReq("60"))
	wantEq(t, "the full Ping", root.ChildText(wbxml.PGStatus), "1")

	// Heartbeat only: the cached Inbox is watched, so the expiry is Status 1.
	_, root = postCommand(t, ts, "Ping", wbxml.Elem(wbxml.PGPing, wbxml.Str(wbxml.PGHeartbeatInt, "60")))
	wantEq(t, "a Ping with only a heartbeat", root.ChildText(wbxml.PGStatus), "1")

	// Empty: the cached Inbox and heartbeat are used, and new mail is reported.
	seedInbox(t, dir, 1)
	root = postEmptyPing(t, ts)
	wantEq(t, "an empty Ping after new mail", root.ChildText(wbxml.PGStatus), "2")
	if f := root.Child(wbxml.PGFolders); f == nil || f.ChildText(wbxml.PGFolder) != inboxID() {
		t.Error("the empty Ping did not report the cached Inbox")
	}
}

// TestPingRaisesACachedHeartbeatBelowTheFloor proves the 60-second heartbeat
// floor also holds for a heartbeat cached before it existed: an empty Ping
// reuses the device's cached interval (MS-ASCMD 3.1.5.8), and a cached 30
// seconds is held to 60 rather than letting the device keep polling faster
// than the floor allows.
func TestPingRaisesACachedHeartbeatBelowTheFloor(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	mustNoErr(t, "cache an old heartbeat", updateState(st, func(s *asState) bool {
		d := s.device("dev1")
		d.PingHeartbeat, d.PingFolders = 30, []string{inboxID()}
		return true
	}))
	st.Close()

	wantEq(t, "the empty Ping", postEmptyPing(t, ts).ChildText(wbxml.PGStatus), "1")
	st, err = objectstore.Open(dir)
	mustNoErr(t, "reopen the store", err)
	defer st.Close()
	state, err := loadState(st)
	mustNoErr(t, "load the state", err)
	wantEq(t, "the cached heartbeat after the Ping", state.device("dev1").PingHeartbeat, int64(60))
}

// TestPingNoFolders confirms a Ping naming no folder to watch reports Status 3.
func TestPingNoFolders(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "Ping", wbxml.Elem(wbxml.PGPing, wbxml.Str(wbxml.PGHeartbeatInt, "60")))
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

	_, root := postCommand(t, ts, "Ping", pingReq("60"))
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

	_, root := postCommand(t, ts, "Ping", pingReq("60"))
	if root.ChildText(wbxml.PGStatus) != "1" {
		t.Errorf("status = %q, want 1 (heartbeat expired)", root.ChildText(wbxml.PGStatus))
	}
}

// TestPingWatchesTheCalendar proves Ping reads a calendar folder as the object
// folder Sync serves ([MS-ASCMD] 2.2.1.13: Status 1 when nothing changed since the
// last Sync, Status 2 naming the folder when it did). An appointment is not in the
// mail index, so reading the folder as mail sees the synced appointment as gone.
func TestPingWatchesTheCalendar(t *testing.T) {
	ts, dir := seededServer(t)
	cal := strconv.Itoa(int(mapi.PrivateFIDCalendar))
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	seedAppointment(t, dir, "Standup", start, start.Add(30*time.Minute))
	syncCal := func(key string) {
		postCommand(t, ts, "Sync", wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections,
			wbxml.Elem(wbxml.ASCollection, wbxml.Str(wbxml.ASSyncKey, key), wbxml.Str(wbxml.ASCollectionID, cal)))))
	}
	syncCal("0")
	syncCal("1")
	ping := wbxml.Elem(wbxml.PGPing, wbxml.Str(wbxml.PGHeartbeatInt, "60"),
		wbxml.Elem(wbxml.PGFolders, wbxml.Elem(wbxml.PGFolder,
			wbxml.Str(wbxml.PGID, cal), wbxml.Str(wbxml.PGClass, "Calendar"))))

	_, root := postCommand(t, ts, "Ping", ping)
	wantEq(t, "Ping status with nothing changed", root.ChildText(wbxml.PGStatus), "1")

	seedAppointment(t, dir, "Review", start.Add(time.Hour), start.Add(2*time.Hour))
	_, root = postCommand(t, ts, "Ping", ping)
	wantEq(t, "Ping status after a new appointment", root.ChildText(wbxml.PGStatus), "2")
}

// TestPingDetectsChange confirms a new message makes Ping return Status 2 with
// the changed folder, without waiting out the heartbeat.
func TestPingDetectsChange(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))
	seedInbox(t, dir, 1) // a new message arrives after the sync

	_, root := postCommand(t, ts, "Ping", pingReq("60"))
	if root.ChildText(wbxml.PGStatus) != "2" {
		t.Fatalf("status = %q, want 2 (changes)", root.ChildText(wbxml.PGStatus))
	}
	folders := root.Child(wbxml.PGFolders)
	if folders == nil || folders.ChildText(wbxml.PGFolder) != inboxID() {
		t.Errorf("Ping did not report the Inbox as changed")
	}
}
